package wire

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

func TestStatusClass(t *testing.T) {
	cases := map[int]domain.FailureClass{
		200: domain.ClassOK,
		204: domain.ClassOK,
		400: domain.ClassCaller,
		401: domain.ClassCredential,
		403: domain.ClassCredential,
		404: domain.ClassModelAccess,
		413: domain.ClassCaller,
		429: domain.ClassRateLimit,
		500: domain.ClassTransient,
		503: domain.ClassTransient,
		529: domain.ClassTransient,
		302: domain.ClassCaller,
	}
	for code, want := range cases {
		class, err := StatusClass(code)
		if class != want || (class == domain.ClassOK) != (err == nil) {
			t.Errorf("status %d: class %q, err %v; want %q", code, class, err, want)
		}
	}
}

func TestClassForErr(t *testing.T) {
	if got := ClassForErr(nil); got != domain.ClassOK {
		t.Errorf("nil: %q", got)
	}
	if got := ClassForErr(context.Canceled); got != domain.ClassCanceled {
		t.Errorf("canceled: %q", got)
	}
	if got := ClassForErr(errors.New("i/o Timeout")); got != domain.ClassTimeout {
		t.Errorf("timeout: %q", got)
	}
	if got := ClassForErr(errors.New("connection reset")); got != domain.ClassTransient {
		t.Errorf("reset: %q", got)
	}
}

func TestClientUsesTheSetClientAndGuardsTheRest(t *testing.T) {
	set := &http.Client{}
	if got := Client(set, nil, domain.PlanMetered); got != set {
		t.Fatal("a client set by the adapter was replaced")
	}
	if Client(nil, nil, domain.PlanLocal) != localClient {
		t.Error("a local plan does not get the local client")
	}
	for _, plan := range []string{domain.PlanMetered, domain.PlanSubscription, ""} {
		if Client(nil, nil, plan) != strictClient {
			t.Errorf("plan %q does not get the strict client", plan)
		}
	}
	lookup := func(context.Context, string, string) ([]netip.Addr, error) { return nil, nil }
	if got := Client(nil, lookup, domain.PlanMetered); got == strictClient || got == localClient {
		t.Error("a lookup did not make its own client")
	}
}
