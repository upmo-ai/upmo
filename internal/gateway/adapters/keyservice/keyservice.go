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
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/keys"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
)

// Rate is how many wrap, unwrap and provider-test calls one audience may
// make per Window, counted together in this process's memory.
const (
	Rate   = 100
	Window = 10 * time.Second
)

// PasswordRate is how many password-verify calls brain may make per
// Window, counted apart from Rate. PasswordAttempts is how many checks one
// address may take per 60-second window, counted in the database so a
// restart keeps them.
const (
	PasswordRate     = 10
	PasswordAttempts = 5
)

// The bounds of a password-verify body, in bytes.
const (
	maxEmailBytes    = 320
	maxPasswordBytes = 1024
)

// The audit actions this service writes through audit.record_keys.
const (
	actionWrap           = "keys.wrap"
	actionUnwrap         = "keys.unwrap"
	actionProviderTest   = "keys.provider_test"
	actionPasswordVerify = "keys.password_verify"
)

var (
	// ErrHashBusy is a check refused because every hash slot is in use; the
	// caller tries again.
	ErrHashBusy = errors.New("keys: hash busy")
	// ErrStoredPassword is a stored password that does not open: not five
	// fields, an unknown key, or altered bytes.
	ErrStoredPassword = errors.New("keys: stored password")
)

// PasswordAccount is the person a password check is for. Subject is the
// address's window key, the current 32-byte email index MAC. User is empty
// for an unknown address, Stored is empty when no password is set, and
// Usable is true only for an enabled person with a stored password.
type PasswordAccount struct {
	Subject []byte
	User    string
	Stored  string
	Usable  bool
}

// Passwords is what password-verify needs from identity, on the owner pool.
// Check runs exactly one Argon2 verify on every path: against the stored
// hash when the account is usable, otherwise a dummy one, and then answers
// no match. It returns ErrStoredPassword or ErrHashBusy, and never the hash.
type Passwords interface {
	ByAddress(ctx context.Context, email string) (PasswordAccount, error)
	// BySession is the enabled person behind an unexpired session hash;
	// false for any other hash.
	BySession(ctx context.Context, tokenHash []byte) (PasswordAccount, bool, error)
	// Take counts one check for subject and returns the window's count.
	Take(ctx context.Context, subject []byte) (int, error)
	Check(ctx context.Context, account PasswordAccount, password string) (match, rehash bool, err error)
}

// proxySecretLen is the size of a sandbox's proxy secret, and actorHashLen
// the size of a session or API token hash.
const (
	proxySecretLen = 32
	actorHashLen   = sha256.Size
)

var (
	refPattern     = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	sandboxPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	// personPattern is a password ref: a person's uuid in lower case.
	personPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
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
// wrap columns and writes the audit rows; test serves provider-test and
// passwords serves password-verify. A missing pool, ring, tester, password
// check or audience key is an error, so the worker cannot serve a route
// that fails open.
func New(owner *pgxpool.Pool, ring domain.Keyring, verify Verify, test ProviderTester, passwords Passwords, log *slog.Logger) (http.Handler, error) {
	if owner == nil {
		return nil, errors.New("keyservice: no database")
	}
	if test == nil {
		return nil, errors.New("keyservice: no provider tester")
	}
	if passwords == nil {
		return nil, errors.New("keyservice: no password check")
	}
	if !ring.Ready() {
		return nil, domain.ErrNoMasterKey
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &server{
		owner: owner, ring: ring, test: test, passwords: passwords, log: log,
		limit:         &limiter{rate: Rate, at: map[string][]time.Time{}},
		passwordLimit: &limiter{rate: PasswordRate, at: map[string][]time.Time{}},
	}
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
	owner     *pgxpool.Pool
	ring      domain.Keyring
	test      ProviderTester
	passwords Passwords
	log       *slog.Logger
	limit     *limiter
	// passwordLimit counts password-verify calls apart from limit.
	passwordLimit *limiter
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
		case r.Method == http.MethodPost && r.URL.Path == keys.PathPasswordVerify:
			s.passwordVerify(w, r, audience)
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
	// Brain alone wraps: a credential for its ref, or a password for the
	// person's uuid.
	if audience != svcsig.AudienceWorkerBrain || !mayWrap(in.Kind, in.Ref) {
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

// mayWrap is the wrap rule for brain's kinds and refs.
func mayWrap(kind, ref string) bool {
	switch kind {
	case domain.KindCredential:
		return refPattern.MatchString(ref)
	case domain.KindPassword:
		return personPattern.MatchString(ref)
	default:
		return false
	}
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
	// A failed probe lists no models. An OK one keeps nil (the adapter lists
	// none, like Vertex) apart from empty (it listed none), so brain clears
	// the seen models only for the second.
	if !out.OK && out.Models == nil {
		out.Models = []json.RawMessage{}
	}
	respond(w, out)
}

// passwordVerifyBody is the password-verify body as read: the two proofs
// stay raw so a null counts as present, and exactly one must be.
type passwordVerifyBody struct {
	Email          json.RawMessage `json:"email"`
	ActorTokenHash json.RawMessage `json:"actor_token_hash"`
	Password       string          `json:"password"`
}

// passwordVerify checks a password for brain, by address at sign-in or for
// the signed-in person behind a session hash. The worker opens the stored
// hash and answers match and rehash only. Each address takes at most
// PasswordAttempts checks a minute, unknown ones included, so a refusal
// never tells whether an address exists.
func (s *server) passwordVerify(w http.ResponseWriter, r *http.Request, audience string) {
	ctx := r.Context()
	changes := map[string]string{"audience": audience, "kind": domain.KindPassword}
	if !s.passwordLimit.take(audience, time.Now()) {
		s.deny(w, ctx, http.StatusTooManyRequests, actionPasswordVerify, audience, changes)
		return
	}
	var in passwordVerifyBody
	if !decode(r, &in) || audience != svcsig.AudienceWorkerBrain {
		s.deny(w, ctx, http.StatusForbidden, actionPasswordVerify, audience, changes)
		return
	}
	email, tokenHash, raw, ok := in.proof()
	if !ok || len(in.Password) == 0 || len(in.Password) > maxPasswordBytes {
		if raw != nil {
			changes["ref_digest"] = s.ring.AuditDigest(raw)
		}
		s.deny(w, ctx, http.StatusForbidden, actionPasswordVerify, audience, changes)
		return
	}
	var account PasswordAccount
	var err error
	if tokenHash == nil {
		account, err = s.passwords.ByAddress(ctx, email)
	} else {
		var found bool
		account, found, err = s.passwords.BySession(ctx, tokenHash)
		if err == nil && !found {
			s.deny(w, ctx, http.StatusForbidden, actionPasswordVerify, audience, changes)
			return
		}
	}
	if err != nil || len(account.Subject) != sha256.Size {
		s.log.Warn("keys: find a password account", "err", err)
		s.deny(w, ctx, http.StatusServiceUnavailable, actionPasswordVerify, audience, changes)
		return
	}
	attempts, err := s.passwords.Take(ctx, account.Subject)
	if err != nil {
		s.log.Warn("keys: count a password check", "err", err)
		s.deny(w, ctx, http.StatusServiceUnavailable, actionPasswordVerify, audience, changes)
		return
	}
	if attempts > PasswordAttempts {
		s.deny(w, ctx, http.StatusTooManyRequests, actionPasswordVerify, audience, changes)
		return
	}
	match, rehash, err := s.passwords.Check(ctx, account, in.Password)
	switch {
	case errors.Is(err, ErrHashBusy):
		s.deny(w, ctx, http.StatusServiceUnavailable, actionPasswordVerify, audience, changes)
		return
	case err != nil:
		// Name the key only when the value has its shape: field 1 of a
		// stored value is never logged otherwise.
		if parts := strings.Split(account.Stored, ":"); len(parts) == 5 && domain.ValidKeyID(parts[0]) {
			s.log.Warn("keys: a stored password did not open", "key_id", parts[0])
		} else {
			s.log.Warn("keys: a stored password did not open")
		}
		s.deny(w, ctx, http.StatusInternalServerError, actionPasswordVerify, audience, changes)
		return
	}
	if err := s.record(ctx, actionPasswordVerify, "success", audience, changes); err != nil {
		s.log.Warn("keys: audit refused a password check", "err", err)
		refuse(w, http.StatusForbidden)
		return
	}
	respond(w, keys.PasswordVerifyResponse{Match: match, Rehash: rehash})
}

// proof reads exactly one of the address and the session hash. raw is the
// caller's bytes of the proof it named, for a digest when they are refused.
func (b passwordVerifyBody) proof() (email string, tokenHash, raw []byte, ok bool) {
	hasEmail, hasHash := b.Email != nil, b.ActorTokenHash != nil
	switch {
	case hasEmail:
		if err := json.Unmarshal(b.Email, &email); err != nil {
			return "", nil, []byte(b.Email), false
		}
		raw = []byte(email)
		if hasHash || len(email) == 0 || len(email) > maxEmailBytes {
			return "", nil, raw, false
		}
		return email, nil, raw, true
	case hasHash:
		if err := json.Unmarshal(b.ActorTokenHash, &tokenHash); err != nil {
			return "", nil, []byte(b.ActorTokenHash), false
		}
		if len(tokenHash) != actorHashLen {
			return "", nil, tokenHash, false
		}
		return "", tokenHash, tokenHash, true
	default:
		return "", nil, nil, false
	}
}

// describe fills the audit changes from the caller's fields. A ref or
// sandbox id that misses its pattern is kept only as a keyed digest.
func (s *server) describe(changes map[string]string, kind, ref, sandboxID string) {
	switch kind {
	case domain.KindCredential, "password", "email":
		changes["kind"] = kind
	}
	if refPattern.MatchString(ref) || (kind == domain.KindPassword && personPattern.MatchString(ref)) {
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

// record writes changes in the audit.Changes shape the sealer reads, each
// value as its "to".
func (s *server) record(ctx context.Context, action, outcome, audience string, changes map[string]string) error {
	stored := audit.Changes{Fields: make([]string, 0, len(changes)), Values: make(map[string]audit.Change, len(changes))}
	for field, value := range changes {
		stored.Fields = append(stored.Fields, field)
		stored.Values[field] = audit.Change{To: value}
	}
	sort.Strings(stored.Fields)
	raw, err := json.Marshal(stored)
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
	return errors.Is(dec.Decode(&extra), io.EOF)
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
	mu   sync.Mutex
	rate int
	at   map[string][]time.Time
}

// take records one call and reports whether it is within rate. A refused
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
	if len(kept) >= l.rate {
		l.at[audience] = kept
		return false
	}
	l.at[audience] = append(kept, now)
	return true
}
