// Package wire holds what every HTTP provider adapter shares: the guarded
// client that refuses forbidden addresses, and the mapping from a provider's
// answer to a failure class.
package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/netguard"
)

// Lookup resolves host names for the guarded clients. Tests set one to
// re-point a name.
type Lookup = func(ctx context.Context, network, host string) ([]netip.Addr, error)

// The guarded clients. Every connection resolves the host and refuses the
// dial when any address is one the account's plan forbids, then dials the
// vetted address, so a name cannot be re-pointed after a URL was saved. Only
// a local account may reach a private or loopback address; none may reach a
// link-local or metadata address. A redirect is never followed: the answer
// is the redirect itself, so a key is not sent on to another place.
var (
	strictClient = guardedClient(false, nil)
	localClient  = guardedClient(true, nil)
)

func guardedClient(local bool, lookup Lookup) *http.Client {
	g := &netguard.Guard{LocalNetwork: local, Lookup: lookup}
	return &http.Client{
		Transport: g.Transport(),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Client returns the HTTP client for a call on an account of the given plan.
// A client set by the adapter (a test's) is used as it is. Otherwise the
// call goes through a guarded client: with lookup when one is set, and else
// with the system resolver.
func Client(set *http.Client, lookup Lookup, plan string) *http.Client {
	if set != nil {
		return set
	}
	local := plan == domain.PlanLocal
	if lookup != nil {
		return guardedClient(local, lookup)
	}
	if local {
		return localClient
	}
	return strictClient
}

// StatusClass maps an HTTP status to the failure class of the call. A 2xx
// status is ClassOK with no error.
func StatusClass(code int) (domain.FailureClass, error) {
	switch {
	case code == http.StatusTooManyRequests:
		return domain.ClassRateLimit, fmt.Errorf("gateway: provider %d", code)
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return domain.ClassCredential, fmt.Errorf("gateway: provider %d", code)
	case code == http.StatusNotFound:
		return domain.ClassModelAccess, fmt.Errorf("gateway: provider %d", code)
	case code >= 500:
		return domain.ClassTransient, fmt.Errorf("gateway: provider %d", code)
	case code < 200 || code >= 300:
		return domain.ClassCaller, fmt.Errorf("gateway: provider %d", code)
	default:
		return domain.ClassOK, nil
	}
}

// MaxModelListBytes caps how many bytes Probe reads from a provider's model
// list endpoint. Responses beyond this cap are refused as oversized.
const MaxModelListBytes = 4 << 20

// ReadModelList reads a bounded model-list response body. Empty or oversized
// bodies return an error.
func ReadModelList(r io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxModelListBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > MaxModelListBytes {
		return nil, fmt.Errorf("gateway: model list size")
	}
	return raw, nil
}

// DecodeStrictObject decodes a single JSON object into dst and refuses arrays,
// primitives, null, or trailing tokens.
func DecodeStrictObject(raw []byte, dst any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("gateway: model list shape")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("gateway: model list trailing data")
	}
	return nil
}

// ClassForErr maps a transport error to the failure class of the call.
func ClassForErr(err error) domain.FailureClass {
	if err == nil {
		return domain.ClassOK
	}
	if errors.Is(err, context.Canceled) {
		return domain.ClassCanceled
	}
	if strings.Contains(strings.ToLower(err.Error()), "timeout") {
		return domain.ClassTimeout
	}
	return domain.ClassTransient
}
