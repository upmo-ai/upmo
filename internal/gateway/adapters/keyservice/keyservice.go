// Package keyservice serves the worker's key routes: the one place a data
// key is wrapped or unwrapped under the master key. Each route checks the
// caller's audience, writes its audit row before any key bytes leave, and
// keeps nothing once the response is written.
package keyservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/keys"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
)

// Rate is how many wrap, unwrap and provider-test calls one audience may
// make per Window, counted together in this process's memory.
const (
	Rate   = 100
	Window = 10 * time.Second
)

// The audit actions this service writes through audit.record_keys.
const (
	actionWrap   = "keys.wrap"
	actionUnwrap = "keys.unwrap"
)

var (
	refPattern     = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	sandboxPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	// service is the audit service each worker audience acts for.
	service = map[string]string{
		svcsig.AudienceWorkerBrain:    "brain",
		svcsig.AudienceWorkerGateway:  "gateway",
		svcsig.AudienceWorkerSandboxd: "sandboxd",
	}
)

// Verify holds the keys the worker verifies, by audience: the current key
// first, then the previous one. Every audience needs at least one.
type Verify map[string][][]byte

// New returns the handler for /internal/keys/*. The owner pool reads the
// wrap columns and writes the audit rows. A missing pool, an empty ring or a
// missing audience key is an error, so the worker cannot serve a route that
// fails open.
func New(owner *pgxpool.Pool, ring domain.Keyring, verify Verify, log *slog.Logger) (http.Handler, error) {
	if owner == nil {
		return nil, errors.New("keyservice: no database")
	}
	if !ring.Ready() {
		return nil, domain.ErrNoMasterKey
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &server{owner: owner, ring: ring, log: log, limit: &limiter{at: map[string][]time.Time{}}}
	d := dispatch{}
	for audience := range service {
		h, err := svcsig.Handler(verify[audience], audience, keys.MaxBody, s.routes(audience))
		if err != nil {
			return nil, errors.New("keyservice: " + audience + " key")
		}
		d[audience] = h
	}
	return d, nil
}

// dispatch hands a request to the verifier of the audience its bearer names,
// which checks the signature, the nonce and the body. An unsigned call or an
// audience the worker does not serve is 401.
type dispatch map[string]http.Handler

func (d dispatch) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	rest, _ := strings.CutPrefix(bearer, svcsig.BearerPrefixV2)
	audience, _, _ := strings.Cut(rest, ".")
	h := d[audience]
	if h == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	h.ServeHTTP(w, r)
}

type server struct {
	owner *pgxpool.Pool
	ring  domain.Keyring
	log   *slog.Logger
	limit *limiter
}

// routes serves one verified audience.
func (s *server) routes(audience string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == keys.PathReady:
			if !s.ring.Ready() {
				http.Error(w, "not ready", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte("ok"))
		case r.Method == http.MethodPost && r.URL.Path == keys.PathWrap:
			s.wrap(w, r, audience)
		case r.Method == http.MethodPost && r.URL.Path == keys.PathUnwrap:
			s.unwrap(w, r, audience)
		default:
			http.NotFound(w, r)
		}
	})
}

func (s *server) wrap(w http.ResponseWriter, r *http.Request, audience string) {
	changes := map[string]string{"audience": audience}
	if !s.limit.take(audience, time.Now()) {
		s.deny(w, r.Context(), http.StatusTooManyRequests, actionWrap, audience, changes)
		return
	}
	var in keys.WrapRequest
	if !decode(r, &in) {
		s.deny(w, r.Context(), http.StatusForbidden, actionWrap, audience, changes)
		return
	}
	s.describe(changes, in.Kind, in.Ref, "")
	// Brain alone wraps, and only credentials until passwords and email move.
	if audience != svcsig.AudienceWorkerBrain || in.Kind != domain.KindCredential || !refPattern.MatchString(in.Ref) {
		s.deny(w, r.Context(), http.StatusForbidden, actionWrap, audience, changes)
		return
	}
	if err := s.record(r.Context(), actionWrap, "success", audience, changes); err != nil {
		s.log.Warn("keys: audit refused a wrap", "err", err)
		refuse(w, http.StatusForbidden)
		return
	}
	made, err := s.ring.WrapNew(in.Kind, in.Ref)
	if err != nil {
		refuse(w, http.StatusForbidden)
		return
	}
	defer clear(made.DataKey)
	respond(w, keys.WrapResponse{KeyID: made.KeyID, DataKey: made.DataKey, WrapNonce: made.WrapNonce, WrappedKey: made.WrappedKey})
}

func (s *server) unwrap(w http.ResponseWriter, r *http.Request, audience string) {
	ctx := r.Context()
	changes := map[string]string{"audience": audience}
	if !s.limit.take(audience, time.Now()) {
		s.deny(w, ctx, http.StatusTooManyRequests, actionUnwrap, audience, changes)
		return
	}
	var in keys.UnwrapRequest
	if !decode(r, &in) {
		s.deny(w, ctx, http.StatusForbidden, actionUnwrap, audience, changes)
		return
	}
	s.describe(changes, in.Kind, in.Ref, in.SandboxID)
	// Gateway unwraps a credential with no sandbox and no proxy secret.
	// Brain never unwraps; sandboxd's proof lands with its proxy.
	if audience != svcsig.AudienceWorkerGateway || in.Kind != domain.KindCredential || !refPattern.MatchString(in.Ref) ||
		in.SandboxID != "" || len(in.ProxySecret) != 0 {
		s.deny(w, ctx, http.StatusForbidden, actionUnwrap, audience, changes)
		return
	}
	var keyID string
	var nonce, wrapped []byte
	err := s.owner.QueryRow(ctx, `SELECT key_id, wrap_nonce, wrapped_key FROM gateway.credentials WHERE ref = $1`, in.Ref).
		Scan(&keyID, &nonce, &wrapped)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.Warn("keys: read a wrapped key", "err", err)
		}
		s.deny(w, ctx, http.StatusForbidden, actionUnwrap, audience, changes)
		return
	}
	if err := s.record(ctx, actionUnwrap, "success", audience, changes); err != nil {
		s.log.Warn("keys: audit refused an unwrap", "err", err)
		refuse(w, http.StatusForbidden)
		return
	}
	dataKey, err := s.ring.Unwrap(in.Kind, in.Ref, keyID, nonce, wrapped)
	if err != nil {
		s.log.Warn("keys: a wrapped key did not open", "key_id", keyID)
		refuse(w, http.StatusForbidden)
		return
	}
	defer clear(dataKey)
	respond(w, keys.UnwrapResponse{DataKey: dataKey})
}

// describe fills the audit changes from the caller's fields. A ref or
// sandbox id that misses its pattern is kept only as a keyed digest.
func (s *server) describe(changes map[string]string, kind, ref, sandboxID string) {
	switch kind {
	case domain.KindCredential, "password", "email":
		changes["kind"] = kind
	}
	if refPattern.MatchString(ref) {
		changes["ref"] = ref
	} else {
		changes["ref_digest"] = s.ring.AuditDigest([]byte(ref))
	}
	switch {
	case sandboxID == "":
	case sandboxPattern.MatchString(sandboxID):
		changes["sandbox_id"] = sandboxID
	case changes["ref_digest"] == "":
		changes["ref_digest"] = s.ring.AuditDigest([]byte(sandboxID))
	}
}

// deny writes the refusal's audit row, then the refusal. A failed audit
// write changes nothing for the caller: no key leaves either way.
func (s *server) deny(w http.ResponseWriter, ctx context.Context, status int, action, audience string, changes map[string]string) {
	if err := s.record(ctx, action, "denied", audience, changes); err != nil {
		s.log.Warn("keys: audit refused a denial", "action", action, "err", err)
	}
	refuse(w, status)
}

func (s *server) record(ctx context.Context, action, outcome, audience string, changes map[string]string) error {
	raw, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	_, err = s.owner.Exec(ctx, `SELECT audit.record_keys($1, $2, $3, $4::jsonb)`, action, outcome, service[audience], string(raw))
	return err
}

// decode reads exactly one JSON object with no unknown field.
func decode(r *http.Request, into any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return false
	}
	var extra json.RawMessage
	return dec.Decode(&extra) != nil
}

// refuse writes the one refusal body every key route uses.
func refuse(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte("keys: refused"))
}

// respond writes v as JSON and wipes the encoded bytes after.
func respond(w http.ResponseWriter, v any) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		refuse(w, http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(buf.Bytes())
	clear(buf.Bytes())
}

// limiter counts calls per audience over the last Window.
type limiter struct {
	mu sync.Mutex
	at map[string][]time.Time
}

// take records one call and reports whether it is within Rate. A refused
// call is not recorded.
func (l *limiter) take(audience string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.at[audience][:0]
	for _, t := range l.at[audience] {
		if now.Sub(t) < Window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= Rate {
		l.at[audience] = kept
		return false
	}
	l.at[audience] = append(kept, now)
	return true
}
