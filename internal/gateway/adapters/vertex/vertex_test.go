package vertex

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

func testServiceAccount(t *testing.T, email, keyID, extraTokenURI string) (string, *rsa.PublicKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	payload := map[string]string{
		"type":           "service_account",
		"client_email":   email,
		"private_key":    pemText,
		"private_key_id": keyID,
	}
	if extraTokenURI != "" {
		payload["token_uri"] = extraTokenURI
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw), &priv.PublicKey
}

func TestTokenMintJWTClaimsAndIgnoresJSONTokenURI(t *testing.T) {
	var evilHits atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		evilHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(evil.Close)

	saJSON, pubKey := testServiceAccount(t, "bot@demo.iam.gserviceaccount.com", "keyid1234567890", evil.URL)
	fixedNow := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

	var gotGrant, gotAssertion string
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			t.Errorf("unexpected token request: %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		vals, err := url.ParseQuery(string(body))
		if err != nil {
			t.Errorf("parse form: %v", err)
		}
		gotGrant = vals.Get("grant_type")
		gotAssertion = vals.Get("assertion")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"access_token":"test-minted-token","expires_in":3600,"token_type":"Bearer"}`)
	}))
	t.Cleanup(tokenSrv.Close)

	var gotAuth, gotPath string
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"hello"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	}))
	t.Cleanup(apiSrv.Close)

	a := NewAdapter()
	a.Client = apiSrv.Client()
	a.TokenURL = tokenSrv.URL
	a.Now = func() time.Time { return fixedNow }

	res, class, err := a.Chat(context.Background(), app.Attempt{
		Adapter:  domain.AdapterVertex,
		BaseURL:  apiSrv.URL + "/v1/projects/demo/locations/global/endpoints/openapi",
		Model:    "google/gemini-2.5-flash",
		Secret:   saJSON,
		PlanKind: domain.PlanLocal,
	}, domain.Request{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}})
	if err != nil || class != domain.ClassOK || res.Text != "hello" {
		t.Fatalf("Chat = %+v class=%s err=%v", res, class, err)
	}
	if evilHits.Load() != 0 {
		t.Fatalf("SSRF: adapter called JSON token_uri %d times", evilHits.Load())
	}
	if gotAuth != "Bearer test-minted-token" {
		t.Fatalf("Authorization = %q, want Bearer test-minted-token", gotAuth)
	}
	if gotPath != "/v1/projects/demo/locations/global/endpoints/openapi/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotGrant != jwtGrantType {
		t.Fatalf("grant_type = %q, want %q", gotGrant, jwtGrantType)
	}

	parts := strings.Split(gotAssertion, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT parts = %d", len(parts))
	}
	enc := base64.RawURLEncoding
	hb, err := enc.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	cb, err := enc.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	sig, err := enc.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pubKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("RSA signature verify failed: %v", err)
	}
	var header map[string]string
	if err := json.Unmarshal(hb, &header); err != nil {
		t.Fatal(err)
	}
	if header["alg"] != "RS256" || header["typ"] != "JWT" || header["kid"] != "keyid1234567890" {
		t.Fatalf("JWT header = %+v", header)
	}
	var claims struct {
		Iss   string `json:"iss"`
		Scope string `json:"scope"`
		Aud   string `json:"aud"`
		Iat   int64  `json:"iat"`
		Exp   int64  `json:"exp"`
	}
	if err := json.Unmarshal(cb, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Iss != "bot@demo.iam.gserviceaccount.com" || claims.Scope != oauthScope || claims.Aud != DefaultTokenURL ||
		claims.Iat != fixedNow.Unix() || claims.Exp != fixedNow.Add(time.Hour).Unix() {
		t.Fatalf("JWT claims = %+v", claims)
	}
}

func TestTokenCacheDeduplicationEarlyRefreshAndKeyRotation(t *testing.T) {
	sa1, _ := testServiceAccount(t, "one@demo.iam.gserviceaccount.com", "key111111111111", "")
	sa2, _ := testServiceAccount(t, "two@demo.iam.gserviceaccount.com", "key222222222222", "")

	var mints atomic.Int32
	gate := make(chan struct{})
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := mints.Add(1)
		if n == 1 {
			<-gate
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":3600}`, n)
	}))
	t.Cleanup(tokenSrv.Close)

	var gotTokens sync.Map
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTokens.Store(r.Header.Get("Authorization"), true)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(apiSrv.Close)

	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	a := NewAdapter()
	a.Client = apiSrv.Client()
	a.TokenURL = tokenSrv.URL
	a.Now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}

	attempt := func(secret string) app.Attempt {
		return app.Attempt{
			Adapter:  domain.AdapterVertex,
			BaseURL:  apiSrv.URL + "/v1/projects/demo/locations/us-central1/endpoints/openapi",
			Model:    "google/gemini-2.5-flash",
			Secret:   secret,
			PlanKind: domain.PlanLocal,
		}
	}
	req := domain.Request{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}

	// 5 concurrent callers share a single mint.
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, class, err := a.Chat(context.Background(), attempt(sa1), req)
			if err != nil || class != domain.ClassOK {
				errs <- fmt.Errorf("class=%s err=%v", class, err)
			}
		}()
	}
	time.Sleep(25 * time.Millisecond)
	close(gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if mints.Load() != 1 {
		t.Fatalf("concurrent mints = %d, want 1", mints.Load())
	}

	// Within 55m window: cache hit, still 1 mint.
	clockMu.Lock()
	now = now.Add(54 * time.Minute)
	clockMu.Unlock()
	if _, class, err := a.Chat(context.Background(), attempt(sa1), req); err != nil || class != domain.ClassOK {
		t.Fatalf("cached chat failed: class=%s err=%v", class, err)
	}
	if mints.Load() != 1 {
		t.Fatalf("mints after 54m = %d, want 1", mints.Load())
	}

	// Past 55m (5m early refresh margin): mints tok-2.
	clockMu.Lock()
	now = now.Add(2 * time.Minute)
	clockMu.Unlock()
	if _, class, err := a.Chat(context.Background(), attempt(sa1), req); err != nil || class != domain.ClassOK {
		t.Fatalf("refreshed chat failed: class=%s err=%v", class, err)
	}
	if mints.Load() != 2 {
		t.Fatalf("mints after 56m = %d, want 2", mints.Load())
	}

	// Rotating key to sa2 immediately mints tok-3 (never reuses sa1 token).
	if _, class, err := a.Chat(context.Background(), attempt(sa2), req); err != nil || class != domain.ClassOK {
		t.Fatalf("rotated chat failed: class=%s err=%v", class, err)
	}
	if mints.Load() != 3 {
		t.Fatalf("mints after key rotation = %d, want 3", mints.Load())
	}
	if _, ok := gotTokens.Load("Bearer tok-3"); !ok {
		t.Fatal("rotated account did not send tok-3")
	}
}

func TestStaggered401EvictsOnlyFailedGenerationAndRetriesOnce(t *testing.T) {
	sa, _ := testServiceAccount(t, "bot@demo.iam.gserviceaccount.com", "key1234567890ab", "")
	var mints atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := mints.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":3600}`, n)
	}))
	t.Cleanup(tokenSrv.Close)

	// Two calls both start with cached tok-1. Call 1 gets immediate 401 -> refreshes to tok-2.
	// Call 2's 401 arrives AFTER tok-2 is already cached -> must NOT evict tok-2, must reuse tok-2!
	var tok1Hits atomic.Int32
	secondCanReturn401 := make(chan struct{})
	firstRefreshed := make(chan struct{})
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "Bearer tok-1" {
			n := tok1Hits.Add(1)
			if n == 1 {
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"warm"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
				return
			}
			if n == 3 {
				// Second concurrent caller with stale tok-1: wait until first caller succeeded with tok-2.
				<-secondCanReturn401
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if auth == "Bearer tok-2" {
			select {
			case <-firstRefreshed:
			default:
				close(firstRefreshed)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"recovered"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
			return
		}
		t.Errorf("unexpected token %q (fresh tok-2 was wrongly evicted)", auth)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(apiSrv.Close)

	a := NewAdapter()
	a.Client = apiSrv.Client()
	a.TokenURL = tokenSrv.URL
	attempt := app.Attempt{
		Adapter:  domain.AdapterVertex,
		BaseURL:  apiSrv.URL + "/v1/projects/demo/locations/global/endpoints/openapi",
		Model:    "google/gemini-2.5-flash",
		Secret:   sa,
		PlanKind: domain.PlanLocal,
	}
	req := domain.Request{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}

	// Warm cache with tok-1.
	if _, class, err := a.Chat(context.Background(), attempt, req); err != nil || class != domain.ClassOK {
		t.Fatalf("warm chat failed: class=%s err=%v", class, err)
	}
	if mints.Load() != 1 {
		t.Fatalf("warm mints = %d, want 1", mints.Load())
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	// Caller 1 and caller 2 both read cached tok-1.
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, class, err := a.Chat(context.Background(), attempt, req)
			if err != nil || class != domain.ClassOK || res.Text != "recovered" {
				errs <- fmt.Errorf("res=%+v class=%s err=%v", res, class, err)
			}
		}()
	}
	<-firstRefreshed
	close(secondCanReturn401)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if mints.Load() != 2 {
		t.Fatalf("total mints = %d, want 2 (staggered 401 must not evict newer gen)", mints.Load())
	}

	// Persistent 401 on fresh token must stop after at most 1 retry (no loop).
	always401 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(always401.Close)
	before := mints.Load()
	attempt.BaseURL = always401.URL + "/v1/projects/demo/locations/global/endpoints/openapi"
	_, class, err := a.Chat(context.Background(), attempt, req)
	if err == nil || class != domain.ClassCredential {
		t.Fatalf("always 401: class=%s err=%v", class, err)
	}
	// Cached tok-2 failed -> evicted -> minted tok-3 -> failed -> stopped (only +1 mint).
	if mints.Load() != before+1 {
		t.Fatalf("mints after persistent 401 = %d, want %d", mints.Load(), before+1)
	}
}

func TestOAuthErrorClassificationAndSanitization(t *testing.T) {
	sa, _ := testServiceAccount(t, "secret-email@demo.iam.gserviceaccount.com", "key1234567890ab", "")
	cases := []struct {
		name      string
		status    int
		body      string
		wantClass domain.FailureClass
	}{
		{"400 invalid_grant", http.StatusBadRequest, `{"error":"invalid_grant","error_description":"Invalid JWT Signature SECRET_LEAK"}`, domain.ClassCredential},
		{"400 invalid_client", http.StatusBadRequest, `{"error":"invalid_client","error_description":"SECRET_LEAK"}`, domain.ClassCredential},
		{"400 unauthorized_client", http.StatusBadRequest, `{"error":"unauthorized_client"}`, domain.ClassCredential},
		{"400 invalid_request", http.StatusBadRequest, `{"error":"invalid_request"}`, domain.ClassCredential},
		{"400 other oauth error", http.StatusBadRequest, `{"error":"unsupported_grant_type"}`, domain.ClassCaller},
		{"401 unauthorized", http.StatusUnauthorized, `{"error":"SECRET_LEAK"}`, domain.ClassCredential},
		{"403 forbidden", http.StatusForbidden, `{"error":"SECRET_LEAK"}`, domain.ClassCredential},
		{"429 rate limited", http.StatusTooManyRequests, `{"error":"SECRET_LEAK"}`, domain.ClassRateLimit},
		{"503 unavailable", http.StatusServiceUnavailable, `SECRET_LEAK`, domain.ClassTransient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			t.Cleanup(srv.Close)

			a := NewAdapter()
			a.Client = srv.Client()
			a.TokenURL = srv.URL
			_, class, err := a.Probe(context.Background(), app.Attempt{
				Adapter:  domain.AdapterVertex,
				BaseURL:  "https://aiplatform.googleapis.com/v1/projects/demo/locations/global/endpoints/openapi",
				Secret:   sa,
				PlanKind: domain.PlanMetered,
			})
			if err == nil || class != tc.wantClass {
				t.Fatalf("Probe class=%s want %s err=%v", class, tc.wantClass, err)
			}
			msg := err.Error()
			if strings.Contains(msg, "SECRET_LEAK") || strings.Contains(msg, "PRIVATE KEY") || strings.Contains(msg, "secret-email") {
				t.Fatalf("error leaked sensitive text: %q", msg)
			}
		})
	}
}

func TestProbeBypassesCacheAndReturnsNilModels(t *testing.T) {
	sa, _ := testServiceAccount(t, "bot@demo.iam.gserviceaccount.com", "key1234567890ab", "")
	var revoked atomic.Bool
	var mints atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mints.Add(1)
		if revoked.Load() {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":"invalid_grant","error_description":"account disabled"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"access_token":"tok-fresh","expires_in":3600}`)
	}))
	t.Cleanup(tokenSrv.Close)

	a := NewAdapter()
	a.Client = tokenSrv.Client()
	a.TokenURL = tokenSrv.URL
	attempt := app.Attempt{
		Adapter:  domain.AdapterVertex,
		BaseURL:  "https://aiplatform.googleapis.com/v1/projects/demo/locations/global/endpoints/openapi",
		Secret:   sa,
		PlanKind: domain.PlanMetered,
	}

	models, class, err := a.Probe(context.Background(), attempt)
	if err != nil || class != domain.ClassOK {
		t.Fatalf("Probe 1 = %v %s %v", models, class, err)
	}
	if models != nil {
		t.Fatalf("Probe models = %#v, want explicit nil slice", models)
	}

	// Revoke key while cache is warm: Probe MUST bypass cache and fail with ClassCredential.
	revoked.Store(true)
	_, class, err = a.Probe(context.Background(), attempt)
	if err == nil || class != domain.ClassCredential {
		t.Fatalf("Probe after revoke = class %s err %v, want ClassCredential", class, err)
	}
	if mints.Load() != 2 {
		t.Fatalf("mints = %d, want 2 (Probe must bypass cache)", mints.Load())
	}

	// Invalid Vertex base URL rejected before token exchange.
	badAttempt := attempt
	badAttempt.BaseURL = "https://aiplatform.googleapis.com/v1/projects/demo/locations/us-central1/endpoints/openapi"
	_, class, err = a.Probe(context.Background(), badAttempt)
	if err == nil || class != domain.ClassCaller {
		t.Fatalf("Probe bad base URL = class %s err %v, want ClassCaller", class, err)
	}
	if mints.Load() != 2 {
		t.Fatalf("mints after bad URL = %d, want 2", mints.Load())
	}
}

func TestChatStreamToolsAndDecideDelegation(t *testing.T) {
	sa, _ := testServiceAccount(t, "bot@demo.iam.gserviceaccount.com", "key1234567890ab", "")
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"access_token":"tok-stream","expires_in":3600}`)
	}))
	t.Cleanup(tokenSrv.Close)

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-stream" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w,
				"data: {\"choices\":[{\"delta\":{\"content\":\"hi \",\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"q\\\":\\\"a\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"+
					"data: {\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":4}}\n\n"+
					"data: [DONE]\n\n",
			)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"yes"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	t.Cleanup(apiSrv.Close)

	a := NewAdapter()
	a.Client = apiSrv.Client()
	a.TokenURL = tokenSrv.URL
	attempt := app.Attempt{
		Adapter:  domain.AdapterVertex,
		BaseURL:  apiSrv.URL + "/v1/projects/demo/locations/global/endpoints/openapi",
		Model:    "google/gemini-2.5-flash",
		Secret:   sa,
		PlanKind: domain.PlanLocal,
	}

	var texts []string
	var toolNames []string
	res, class, err := a.ChatStreamTools(context.Background(), attempt, domain.Request{
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "go"}},
	}, app.StreamEvents{
		Text:     func(s string) error { texts = append(texts, s); return nil },
		ToolCall: func(d app.ToolCallDelta) error { toolNames = append(toolNames, d.Name); return nil },
	})
	if err != nil || class != domain.ClassOK || res.Text != "hi " || len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "lookup" || res.Usage.Input != 12 || res.Usage.Output != 4 {
		t.Fatalf("ChatStreamTools = %+v class=%s err=%v", res, class, err)
	}
	if len(texts) != 1 || len(toolNames) != 1 || toolNames[0] != "lookup" {
		t.Fatalf("events texts=%v toolNames=%v", texts, toolNames)
	}

	dec, usage, class, err := a.Decide(context.Background(), attempt, domain.DecideRequest{
		Kind:   domain.DecisionYesNo,
		Prompt: "ok?",
	})
	if err != nil || class != domain.ClassOK || !dec.YesNo || usage.Input != 3 {
		t.Fatalf("Decide = %+v usage=%+v class=%s err=%v", dec, usage, class, err)
	}
}
