// Package keyservice serves the worker's key routes: the one place a data
// key is wrapped or unwrapped under the master key. Each route checks the
// caller's audience, writes its audit row before any key bytes leave, and
// keeps nothing once the response is written.
package keyservice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
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
	actionWrap         = "keys.wrap"
	actionUnwrap       = "keys.unwrap"
	actionProviderTest = "keys.provider_test"
)

// proxySecretLen is the size of a sandbox's proxy secret, and actorHashLen
// the size of a session or API token hash.
const (
	proxySecretLen = 32
	actorHashLen   = sha256.Size
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

// ProviderTester probes a saved provider account with its saved key, for
// the person whose unexpired session or API token hashes to actor, when
// that person may manage the account. Once that rule holds, and before it
// opens the key, it calls allowed, and stops when allowed fails. Any error
// is a refusal. The answer never carries the key.
type ProviderTester func(ctx context.Context, accountID string, actor []byte, allowed func() error) (keys.ProviderTestResponse, error)

// New returns the handler for /internal/keys/*. The owner pool reads the
// wrap columns and writes the audit rows; test serves provider-test. A
// missing pool, ring, tester or audience key is an error, so the worker
// cannot serve a route that fails open.
func New(owner *pgxpool.Pool, ring domain.Keyring, verify Verify, test ProviderTester, log *slog.Logger) (http.Handler, error) {
	if owner == nil {
		return nil, errors.New("keyservice: no database")
	}
	if test == nil {
		return nil, errors.New("keyservice: no provider tester")
	}
	if !ring.Ready() {
		return nil, domain.ErrNoMasterKey
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &server{owner: owner, ring: ring, test: test, log: log, limit: &limiter{at: map[string][]time.Time{}}}
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
	test  ProviderTester
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
		case r.Method == http.MethodPost && r.URL.Path == keys.PathProviderTest:
			s.providerTest(w, r, audience)
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
	if !s.mayUnwrap(ctx, audience, in) {
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

// mayUnwrap is the unwrap rule. Gateway unwraps a credential with no
// sandbox and no proxy secret. Sandboxd unwraps one only for a sandbox whose
// task is live, whose bound refs hold ref and whose stored proxy secret hash
// is the SHA-256 of the secret it sends. Brain never unwraps.
func (s *server) mayUnwrap(ctx context.Context, audience string, in keys.UnwrapRequest) bool {
	if in.Kind != domain.KindCredential || !refPattern.MatchString(in.Ref) {
		return false
	}
	switch audience {
	case svcsig.AudienceWorkerGateway:
		return in.SandboxID == "" && len(in.ProxySecret) == 0
	case svcsig.AudienceWorkerSandboxd:
		if !sandboxPattern.MatchString(in.SandboxID) || len(in.ProxySecret) != proxySecretLen {
			return false
		}
		hash := sha256.Sum256(in.ProxySecret)
		var open bool
		err := s.owner.QueryRow(ctx, `SELECT gateway.sandbox_may_open($1, $2, $3)`, in.SandboxID, in.Ref, hash[:]).Scan(&open)
		if err != nil {
			s.log.Warn("keys: check a sandbox", "err", err)
			return false
		}
		return open
	default:
		return false
	}
}

// providerTest probes a saved account for brain. The adapter and base URL
// come from the stored account; the body names only the account and the
// person, so a caller cannot point the saved key at another address.
func (s *server) providerTest(w http.ResponseWriter, r *http.Request, audience string) {
	ctx := r.Context()
	changes := map[string]string{"audience": audience}
	if !s.limit.take(audience, time.Now()) {
		s.deny(w, ctx, http.StatusTooManyRequests, actionProviderTest, audience, changes)
		return
	}
	var in keys.ProviderTestRequest
	if !decode(r, &in) {
		s.deny(w, ctx, http.StatusForbidden, actionProviderTest, audience, changes)
		return
	}
	if _, err := uuid.Parse(in.AccountID); err == nil && len(in.AccountID) == 36 {
		changes["account_id"] = in.AccountID
	} else {
		changes["ref_digest"] = s.ring.AuditDigest([]byte(in.AccountID))
		s.deny(w, ctx, http.StatusForbidden, actionProviderTest, audience, changes)
		return
	}
	if audience != svcsig.AudienceWorkerBrain || len(in.ActorTokenHash) != actorHashLen {
		s.deny(w, ctx, http.StatusForbidden, actionProviderTest, audience, changes)
		return
	}
	// The success row is written once the person may manage the account and
	// before the saved key is opened; a refusal before that is denied.
	recorded := false
	out, err := s.test(ctx, in.AccountID, in.ActorTokenHash, func() error {
		recorded = true
		return s.record(ctx, actionProviderTest, "success", audience, changes)
	})
	if err != nil {
		if recorded {
			s.log.Warn("keys: a provider test stopped after its audit row", "err", err)
			refuse(w, http.StatusForbidden)
			return
		}
		s.deny(w, ctx, http.StatusForbidden, actionProviderTest, audience, changes)
		return
	}
	if !recorded {
		refuse(w, http.StatusForbidden)
		return
	}
	if out.Models == nil {
		out.Models = []json.RawMessage{}
	}
	respond(w, out)
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
