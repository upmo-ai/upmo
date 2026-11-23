// Package vertex is the adapter for Google Vertex AI's OpenAI-compatible
// endpoint. It mints and caches short-lived OAuth 2.0 access tokens from a
// stored GCP service-account JSON key and delegates chat, streaming, and
// decide calls to the OpenAI-compatible adapter. It has no Embed method.
package vertex

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/openai"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/wire"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

const (
	// DefaultTokenURL is Google's OAuth 2.0 token endpoint. User-supplied
	// service-account JSON token_uri fields are ignored to prevent SSRF.
	DefaultTokenURL = "https://oauth2.googleapis.com/token" //nolint:gosec // public OAuth endpoint URL, not a credential
	oauthScope      = "https://www.googleapis.com/auth/cloud-platform"
	jwtGrantType    = "urn:ietf:params:oauth:grant-type:jwt-bearer"

	chatPath            = "/chat/completions"
	maxTokenBodyBytes   = 64 << 10
	maxCacheEntries     = 256
	maxTokenLifetime    = 24 * time.Hour
	defaultRefreshSkew  = 5 * time.Minute
	tokenRequestTimeout = 15 * time.Second
)

// Compile-time interface assertions. Adapter intentionally omits app.Embedder.
var (
	_ app.Decider           = (*Adapter)(nil)
	_ app.Chatter           = (*Adapter)(nil)
	_ app.StreamChatter     = (*Adapter)(nil)
	_ app.ToolStreamChatter = (*Adapter)(nil)
	_ app.Prober            = (*Adapter)(nil)
)

type cachedToken struct {
	token     string
	gen       uint64
	refreshAt time.Time
	expiresAt time.Time
}

type tokenFlight struct {
	done  chan struct{}
	token string
	gen   uint64
	class domain.FailureClass
	err   error
}

// Adapter talks to Google Vertex AI's OpenAI-compatible chat completions API
// using OAuth 2.0 Bearer tokens minted from a service-account JSON credential.
type Adapter struct {
	// Client overrides the HTTP client used for both token exchange and Vertex
	// API calls. Nil uses wire.Client (guarded via netguard with no redirects).
	Client *http.Client
	// Lookup resolves host names for guarded clients. Nil uses the system resolver.
	Lookup wire.Lookup
	// TokenURL overrides DefaultTokenURL for tests. Production leaves it empty.
	TokenURL string
	// Now returns the current clock time. Nil uses time.Now.
	Now func() time.Time

	mu       sync.Mutex
	cache    map[[32]byte]cachedToken
	inflight map[[32]byte]*tokenFlight
	nextGen  uint64
}

// NewAdapter returns a Vertex adapter with an initialized in-memory token cache.
func NewAdapter() *Adapter {
	return &Adapter{
		cache:    map[[32]byte]cachedToken{},
		inflight: map[[32]byte]*tokenFlight{},
	}
}

func (a *Adapter) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Adapter) tokenEndpoint() string {
	if a.TokenURL != "" {
		return a.TokenURL
	}
	return DefaultTokenURL
}

func (a *Adapter) httpClient(plan string) *http.Client {
	return wire.Client(a.Client, a.Lookup, plan)
}

func (a *Adapter) openAIAdapter() openai.Adapter {
	return openai.Adapter{
		Client:   a.Client,
		Lookup:   a.Lookup,
		ChatPath: chatPath,
	}
}

// Probe validates the Vertex base URL shape and mints a fresh OAuth 2.0 access
// token directly from Google's token endpoint (bypassing cache) so disabled or
// revoked credentials fail immediately. It returns nil models so daily checks
// preserve manually enabled models without retiring them.
func (a *Adapter) Probe(ctx context.Context, attempt app.Attempt) ([]domain.SeenModel, domain.FailureClass, error) {
	cleanBase, err := domain.CleanBaseURLForAdapter(domain.AdapterVertex, attempt.PlanKind, attempt.BaseURL)
	if err != nil || cleanBase == "" {
		return nil, domain.ClassCaller, fmt.Errorf("gateway: vertex base URL is not valid")
	}
	key := sha256.Sum256([]byte(attempt.Secret))
	tok, refreshAt, expiresAt, class, err := a.mintToken(ctx, attempt.PlanKind, attempt.Secret)
	if err != nil {
		if class == domain.ClassCredential {
			a.evictKey(key)
		}
		return nil, class, err
	}
	a.storeToken(key, tok, refreshAt, expiresAt)
	return nil, domain.ClassOK, nil
}

// Decide runs a yes/no or choice decision via Vertex's OpenAI-compatible API.
func (a *Adapter) Decide(ctx context.Context, attempt app.Attempt, req domain.DecideRequest) (domain.DecideResult, domain.Usage, domain.FailureClass, error) {
	key := sha256.Sum256([]byte(attempt.Secret))
	tok, gen, fromCache, class, err := a.token(ctx, attempt.PlanKind, attempt.Secret, key)
	if err != nil {
		return domain.DecideResult{}, domain.Usage{}, class, err
	}
	call := attempt
	call.Secret = tok
	res, usage, class, err := a.openAIAdapter().Decide(ctx, call, req)
	if err != nil && class == domain.ClassCredential && fromCache {
		a.evictGen(key, gen)
		tok, _, _, class, err = a.token(ctx, attempt.PlanKind, attempt.Secret, key)
		if err != nil {
			return domain.DecideResult{}, domain.Usage{}, class, err
		}
		call.Secret = tok
		return a.openAIAdapter().Decide(ctx, call, req)
	}
	return res, usage, class, err
}

// Chat runs a non-streaming chat completion via Vertex's OpenAI-compatible API.
func (a *Adapter) Chat(ctx context.Context, attempt app.Attempt, req domain.Request) (domain.ChatResult, domain.FailureClass, error) {
	key := sha256.Sum256([]byte(attempt.Secret))
	tok, gen, fromCache, class, err := a.token(ctx, attempt.PlanKind, attempt.Secret, key)
	if err != nil {
		return domain.ChatResult{}, class, err
	}
	call := attempt
	call.Secret = tok
	res, class, err := a.openAIAdapter().Chat(ctx, call, req)
	if err != nil && class == domain.ClassCredential && fromCache {
		a.evictGen(key, gen)
		tok, _, _, class, err = a.token(ctx, attempt.PlanKind, attempt.Secret, key)
		if err != nil {
			return domain.ChatResult{}, class, err
		}
		call.Secret = tok
		return a.openAIAdapter().Chat(ctx, call, req)
	}
	return res, class, err
}

// ChatStream streams text deltas from Vertex's OpenAI-compatible API.
func (a *Adapter) ChatStream(ctx context.Context, attempt app.Attempt, req domain.Request, emit func(string) error) (domain.Usage, domain.FailureClass, error) {
	res, class, err := a.ChatStreamTools(ctx, attempt, req, app.StreamEvents{Text: emit})
	return res.Usage, class, err
}

// ChatStreamTools streams text and tool-call deltas from Vertex's
// OpenAI-compatible API. A 401/403 from a cached token evicts that generation
// and retries once before any stream event is emitted.
func (a *Adapter) ChatStreamTools(ctx context.Context, attempt app.Attempt, req domain.Request, events app.StreamEvents) (domain.ChatResult, domain.FailureClass, error) {
	key := sha256.Sum256([]byte(attempt.Secret))
	tok, gen, fromCache, class, err := a.token(ctx, attempt.PlanKind, attempt.Secret, key)
	if err != nil {
		return domain.ChatResult{}, class, err
	}
	emitted := false
	wrapped := app.StreamEvents{
		Text: func(s string) error {
			emitted = true
			if events.Text != nil {
				return events.Text(s)
			}
			return nil
		},
		ToolCall: func(d app.ToolCallDelta) error {
			emitted = true
			if events.ToolCall != nil {
				return events.ToolCall(d)
			}
			return nil
		},
	}
	call := attempt
	call.Secret = tok
	res, class, err := a.openAIAdapter().ChatStreamTools(ctx, call, req, wrapped)
	if err != nil && class == domain.ClassCredential && fromCache && !emitted {
		a.evictGen(key, gen)
		tok, _, _, class, err = a.token(ctx, attempt.PlanKind, attempt.Secret, key)
		if err != nil {
			return domain.ChatResult{}, class, err
		}
		call.Secret = tok
		return a.openAIAdapter().ChatStreamTools(ctx, call, req, events)
	}
	return res, class, err
}

func (a *Adapter) token(ctx context.Context, plan, secret string, key [32]byte) (tok string, gen uint64, fromCache bool, class domain.FailureClass, err error) {
	now := a.now()
	a.mu.Lock()
	if a.cache == nil {
		a.cache = map[[32]byte]cachedToken{}
	}
	if a.inflight == nil {
		a.inflight = map[[32]byte]*tokenFlight{}
	}
	if entry, ok := a.cache[key]; ok && now.Before(entry.refreshAt) {
		a.mu.Unlock()
		return entry.token, entry.gen, true, domain.ClassOK, nil
	}
	if flight, ok := a.inflight[key]; ok {
		a.mu.Unlock()
		select {
		case <-flight.done:
			if flight.err != nil {
				return "", 0, false, flight.class, flight.err
			}
			return flight.token, flight.gen, false, domain.ClassOK, nil
		case <-ctx.Done():
			return "", 0, false, domain.ClassCanceled, ctx.Err()
		}
	}
	flight := &tokenFlight{done: make(chan struct{})}
	a.inflight[key] = flight
	a.mu.Unlock()

	go func() {
		flightCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tokenRequestTimeout)
		defer cancel()
		minted, refreshAt, expiresAt, fclass, ferr := a.mintToken(flightCtx, plan, secret)
		if ferr != nil {
			flight.class = fclass
			flight.err = ferr
		} else {
			flight.token = minted
			flight.gen = a.storeToken(key, minted, refreshAt, expiresAt)
			flight.class = domain.ClassOK
		}
		a.mu.Lock()
		delete(a.inflight, key)
		a.mu.Unlock()
		close(flight.done)
	}()

	select {
	case <-flight.done:
		if flight.err != nil {
			return "", 0, false, flight.class, flight.err
		}
		return flight.token, flight.gen, false, domain.ClassOK, nil
	case <-ctx.Done():
		return "", 0, false, domain.ClassCanceled, ctx.Err()
	}
}

func (a *Adapter) storeToken(key [32]byte, tok string, refreshAt, expiresAt time.Time) uint64 {
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cache == nil {
		a.cache = map[[32]byte]cachedToken{}
	}
	a.nextGen++
	gen := a.nextGen
	if len(a.cache) >= maxCacheEntries {
		for k, v := range a.cache {
			if !now.Before(v.expiresAt) {
				delete(a.cache, k)
			}
		}
		if len(a.cache) >= maxCacheEntries {
			var oldestKey [32]byte
			var oldestTime time.Time
			first := true
			for k, v := range a.cache {
				if first || v.expiresAt.Before(oldestTime) {
					oldestKey = k
					oldestTime = v.expiresAt
					first = false
				}
			}
			if !first {
				delete(a.cache, oldestKey)
			}
		}
	}
	a.cache[key] = cachedToken{
		token:     tok,
		gen:       gen,
		refreshAt: refreshAt,
		expiresAt: expiresAt,
	}
	return gen
}

func (a *Adapter) evictGen(key [32]byte, gen uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if entry, ok := a.cache[key]; ok && entry.gen == gen {
		delete(a.cache, key)
	}
}

func (a *Adapter) evictKey(key [32]byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.cache, key)
}

func (a *Adapter) mintToken(ctx context.Context, plan, secret string) (token string, refreshAt, expiresAt time.Time, class domain.FailureClass, err error) {
	sa, rsaKey, err := domain.ParseVertexServiceAccount(secret)
	if err != nil {
		return "", time.Time{}, time.Time{}, domain.ClassCredential, errors.New("gateway: vertex service account credential is invalid")
	}
	now := a.now().UTC()
	assertion, err := signJWT(sa, rsaKey, now)
	if err != nil {
		return "", time.Time{}, time.Time{}, domain.ClassCredential, errors.New("gateway: vertex jwt signing failed")
	}
	form := url.Values{
		"grant_type": {jwtGrantType},
		"assertion":  {assertion},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.tokenEndpoint(), strings.NewReader(form))
	if err != nil {
		return "", time.Time{}, time.Time{}, domain.ClassCaller, errors.New("gateway: vertex token request invalid")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := a.httpClient(plan).Do(req)
	if err != nil {
		return "", time.Time{}, time.Time{}, wire.ClassForErr(err), errors.New("gateway: vertex token exchange failed")
	}
	defer func() { _ = res.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(res.Body, maxTokenBodyBytes+1))
	if err != nil {
		return "", time.Time{}, time.Time{}, wire.ClassForErr(err), errors.New("gateway: vertex token response read failed")
	}
	if len(raw) > maxTokenBodyBytes {
		return "", time.Time{}, time.Time{}, domain.ClassTransient, errors.New("gateway: vertex token response oversized")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		c, e := classifyOAuthError(res.StatusCode, raw)
		return "", time.Time{}, time.Time{}, c, e
	}
	var parsed struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || strings.TrimSpace(parsed.AccessToken) == "" || parsed.ExpiresIn <= 0 {
		return "", time.Time{}, time.Time{}, domain.ClassTransient, errors.New("gateway: vertex token response malformed")
	}
	lifetime := time.Duration(parsed.ExpiresIn) * time.Second
	if lifetime > maxTokenLifetime {
		lifetime = maxTokenLifetime
	}
	skew := defaultRefreshSkew
	if lifetime <= 2*defaultRefreshSkew {
		skew = lifetime / 2
	}
	expiresAt = now.Add(lifetime)
	refreshAt = expiresAt.Add(-skew)
	return parsed.AccessToken, refreshAt, expiresAt, domain.ClassOK, nil
}

func classifyOAuthError(status int, body []byte) (domain.FailureClass, error) {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return domain.ClassCredential, errors.New("gateway: vertex oauth credential rejected")
	case status == http.StatusTooManyRequests:
		return domain.ClassRateLimit, errors.New("gateway: vertex oauth rate limited")
	case status >= 500:
		return domain.ClassTransient, errors.New("gateway: vertex oauth unavailable")
	case status == http.StatusBadRequest:
		var parsed struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(body), &parsed); err == nil {
			switch strings.ToLower(strings.TrimSpace(parsed.Error)) {
			case "invalid_grant", "invalid_client", "unauthorized_client", "invalid_request":
				return domain.ClassCredential, errors.New("gateway: vertex oauth credential rejected")
			}
		}
		return domain.ClassCaller, errors.New("gateway: vertex oauth request rejected")
	default:
		return domain.ClassCaller, errors.New("gateway: vertex oauth request rejected")
	}
}

func signJWT(sa domain.VertexServiceAccount, key *rsa.PrivateKey, now time.Time) (string, error) {
	header := struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
		Kid string `json:"kid,omitempty"`
	}{
		Alg: "RS256",
		Typ: "JWT",
		Kid: sa.PrivateKeyID,
	}
	claims := struct {
		Iss   string `json:"iss"`
		Scope string `json:"scope"`
		Aud   string `json:"aud"`
		Iat   int64  `json:"iat"`
		Exp   int64  `json:"exp"`
	}{
		Iss:   sa.ClientEmail,
		Scope: oauthScope,
		Aud:   DefaultTokenURL,
		Iat:   now.Unix(),
		Exp:   now.Add(time.Hour).Unix(),
	}
	hb, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(hb) + "." + enc.EncodeToString(cb)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + enc.EncodeToString(sig), nil
}
