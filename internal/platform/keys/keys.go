// Package keys is the client of the worker's key service. Brain, gateway and
// sandboxd hold no master key: brain asks the worker to wrap a new data key,
// and gateway and sandboxd ask it to unwrap the data key of one stored row.
// Each signs with its own worker audience key. Nothing here keeps a key
// after the call that asked for it returns.
package keys

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/platform/tlscheck"
)

// The routes, on the worker's internal listener.
const (
	PathReady  = "/internal/keys/ready"
	PathWrap   = "/internal/keys/wrap"
	PathUnwrap = "/internal/keys/unwrap"
	// PathProviderTest probes a saved provider account with its saved key,
	// which only the worker opens.
	PathProviderTest = "/internal/keys/provider-test"
)

// KindCredential is the one kind wrap and unwrap serve in this release.
const KindCredential = "credential"

// MaxBody caps every request and response body of the key service.
const MaxBody = 4096

// Timeout bounds one call to the worker.
const Timeout = 2 * time.Second

// ProviderTestTimeout bounds one provider-test call: the worker's probe may
// take up to its own 10 s limit, so 2 s would cut every slow provider off.
const ProviderTestTimeout = 12 * time.Second

// MaxProviderTestBody caps a provider-test answer, which carries the model
// list a provider returned (up to 2,000 models).
const MaxProviderTestBody = 8 << 20

// ErrUnavailable is every failed call: a dial error, a timeout and any
// status other than 200. It carries no bytes from the worker.
var ErrUnavailable = errors.New("keys: worker unavailable")

// WrapRequest is the body of POST /internal/keys/wrap.
type WrapRequest struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// WrapResponse is a new data key and its wrap under the current master.
type WrapResponse struct {
	KeyID      string `json:"key_id"`
	DataKey    []byte `json:"data_key"`
	WrapNonce  []byte `json:"wrap_nonce"`
	WrappedKey []byte `json:"wrapped_key"`
}

// UnwrapRequest is the body of POST /internal/keys/unwrap. Gateway sends an
// empty SandboxID and no ProxySecret; sandboxd sends both.
type UnwrapRequest struct {
	Kind        string `json:"kind"`
	Ref         string `json:"ref"`
	SandboxID   string `json:"sandbox_id"`
	ProxySecret []byte `json:"proxy_secret,omitempty"`
}

// UnwrapResponse is the data key of one stored row.
type UnwrapResponse struct {
	DataKey []byte `json:"data_key"`
}

// ProviderTestRequest is the body of POST /internal/keys/provider-test:
// the account and the hash of the signed-in person's session or API token.
// The worker reads the adapter and base URL from the account, never from
// the caller.
type ProviderTestRequest struct {
	AccountID      string `json:"account_id"`
	ActorTokenHash []byte `json:"actor_token_hash"`
}

// ProviderTestResponse is the outcome of a provider-test. Error is the
// fixed probe message, empty when OK. Models are the listed models in the
// stored model list's JSON form. It never carries a key.
type ProviderTestResponse struct {
	OK     bool              `json:"ok"`
	Error  string            `json:"error"`
	Models []json.RawMessage `json:"models"`
}

// Options configure a client.
type Options struct {
	// URL is UPMO_WORKER_URL: https, or http where tlscheck allows it.
	URL string
	// Key is this process's worker audience key, upmo_svc_<audience>_...
	Key string
	// Audience is the worker audience Key belongs to.
	Audience string
	// Development is UPMO_ENV=development.
	Development bool
	// CAFile is UPMO_TLS_CA_FILE, the roots an https worker is checked
	// against. Production requires it for https.
	CAFile string
}

// Client calls the worker's key routes for one audience.
type Client struct {
	base string
	http *http.Client
	// slow is http with the longer provider-test timeout.
	slow *http.Client
}

// Open builds a client. A bad URL, key or CA file is an error, so a process
// cannot start with a client that will never reach the worker.
func Open(o Options) (Client, error) {
	u, err := url.Parse(strings.TrimSpace(o.URL))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return Client{}, fmt.Errorf("config: UPMO_WORKER_URL must be https")
	}
	if u.Scheme == "http" && !tlscheck.PlaintextAllowed(o.Development, u.Hostname()) {
		return Client{}, fmt.Errorf("config: UPMO_WORKER_URL must be https")
	}
	derived, err := svcsig.ParseAudienceKey(o.Key, o.Audience)
	if err != nil {
		return Client{}, err
	}
	var pool *x509.CertPool
	if u.Scheme == "https" && (!o.Development || o.CAFile != "") {
		if pool, err = tlscheck.RootCAs(o.CAFile); err != nil {
			return Client{}, err
		}
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	base.Proxy = nil
	transport, err := svcsig.Transport(base, derived, o.Audience)
	if err != nil {
		return Client{}, err
	}
	// A redirect is a refusal: a key never follows one.
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return Client{
		base: u.Scheme + "://" + u.Host,
		http: &http.Client{Transport: transport, Timeout: Timeout, CheckRedirect: noRedirect},
		slow: &http.Client{Transport: transport, Timeout: ProviderTestTimeout, CheckRedirect: noRedirect},
	}, nil
}

// Ready reports whether the worker's ring is ready and accepts this key.
func (c Client) Ready(ctx context.Context) error {
	body, err := c.call(ctx, c.http, http.MethodGet, PathReady, nil, MaxBody)
	if err != nil {
		return err
	}
	if string(body) != "ok" {
		return ErrUnavailable
	}
	return nil
}

// Wrap asks for a new data key for kind and ref. Only brain's audience may.
// The caller seals with DataKey and then wipes it.
func (c Client) Wrap(ctx context.Context, kind, ref string) (WrapResponse, error) {
	var out WrapResponse
	if err := c.post(ctx, PathWrap, WrapRequest{Kind: kind, Ref: ref}, &out); err != nil {
		return WrapResponse{}, err
	}
	return out, nil
}

// UnwrapCredential asks for the data key of the credential row ref, as
// gateway. The caller opens the payload and then wipes the key.
func (c Client) UnwrapCredential(ctx context.Context, ref string) ([]byte, error) {
	var out UnwrapResponse
	if err := c.post(ctx, PathUnwrap, UnwrapRequest{Kind: KindCredential, Ref: ref}, &out); err != nil {
		return nil, err
	}
	return out.DataKey, nil
}

// UnwrapForSandbox asks for the data key of the credential row ref, as
// sandboxd for sandboxID, proving the sandbox with its 32-byte proxy secret.
// The worker answers only while the sandbox's task is live and the ref is
// bound to it. The caller opens the payload and then wipes the key.
func (c Client) UnwrapForSandbox(ctx context.Context, sandboxID, ref string, proxySecret []byte) ([]byte, error) {
	var out UnwrapResponse
	in := UnwrapRequest{Kind: KindCredential, Ref: ref, SandboxID: sandboxID, ProxySecret: proxySecret}
	if err := c.post(ctx, PathUnwrap, in, &out); err != nil {
		return nil, err
	}
	return out.DataKey, nil
}

// ProviderTest asks the worker to probe a saved account with its saved key,
// for the person whose session or API token hashes to actorTokenHash. A
// refusal, like any failure, is ErrUnavailable.
func (c Client) ProviderTest(ctx context.Context, accountID string, actorTokenHash []byte) (ProviderTestResponse, error) {
	raw, err := json.Marshal(ProviderTestRequest{AccountID: accountID, ActorTokenHash: actorTokenHash})
	if err != nil {
		return ProviderTestResponse{}, ErrUnavailable
	}
	body, err := c.call(ctx, c.slow, http.MethodPost, PathProviderTest, raw, MaxProviderTestBody)
	if err != nil {
		return ProviderTestResponse{}, err
	}
	var out ProviderTestResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return ProviderTestResponse{}, ErrUnavailable
	}
	return out, nil
}

func (c Client) post(ctx context.Context, path string, in, out any) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return ErrUnavailable
	}
	body, err := c.call(ctx, c.http, http.MethodPost, path, raw, MaxBody)
	if err != nil {
		return err
	}
	defer clear(body)
	if err := json.Unmarshal(body, out); err != nil {
		return ErrUnavailable
	}
	return nil
}

// call sends one signed request and returns the body of a 200. Anything else
// is ErrUnavailable with no bytes.
func (c Client) call(ctx context.Context, client *http.Client, method, path string, raw []byte, limit int64) ([]byte, error) {
	if client == nil {
		return nil, ErrUnavailable
	}
	var reader io.Reader = http.NoBody
	if raw != nil {
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return nil, ErrUnavailable
	}
	if raw != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || res.StatusCode != http.StatusOK || int64(len(body)) > limit {
		clear(body)
		return nil, ErrUnavailable
	}
	return body, nil
}
