package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway/app"
)

type stubUsageLimitStore struct {
	setCalls int
}

func (s *stubUsageLimitStore) GetUsageLimits(_ context.Context, _ app.Caller, periodStart time.Time, _ bool) (app.UsageLimitsView, error) {
	return app.UsageLimitsView{PeriodStart: periodStart.Format("2006-01-02")}, nil
}

func (s *stubUsageLimitStore) SetUsageLimit(_ context.Context, _ app.Caller, kind, subjectID string, amount *int64) (app.UpdatedUsageLimit, error) {
	s.setCalls++
	return app.UpdatedUsageLimit{Kind: kind, SubjectID: subjectID, AmountMicroUSD: amount}, nil
}

func TestUsageLimitsHandlerAuthAndBodyValidation(t *testing.T) {
	store := &stubUsageLimitStore{}
	mux := http.NewServeMux()
	UsageLimitsHandler{
		Service: app.UsageLimits{
			Store: store,
			Allow: func(context.Context, string) (string, error) { return "settings:manage", nil },
		},
		Access: func(r *http.Request) (string, bool) {
			u := r.Header.Get("X-User")
			return u, u != ""
		},
	}.MountLimits(mux, nil)

	// Missing session -> 401.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/usage/limits", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth GET = %d, want 401", rec.Code)
	}

	// Bearer token -> 403.
	req := httptest.NewRequest(http.MethodGet, "/api/usage/limits", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bearer GET = %d, want 403", rec.Code)
	}

	// Unknown JSON field -> 400.
	req = httptest.NewRequest(http.MethodPut, "/api/usage/limits", strings.NewReader(`{"kind":"workspace","amount_micro_usd":10,"extra":1}`))
	req.Header.Set("X-User", "018f3b2c-7a10-7c4a-8e11-111111111111")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field PUT = %d, want 400", rec.Code)
	}

	// Trailing JSON -> 400.
	req = httptest.NewRequest(http.MethodPut, "/api/usage/limits", strings.NewReader(`{"kind":"workspace","amount_micro_usd":10}{}`))
	req.Header.Set("X-User", "018f3b2c-7a10-7c4a-8e11-111111111111")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON PUT = %d, want 400", rec.Code)
	}

	// Missing amount_micro_usd key -> 422.
	req = httptest.NewRequest(http.MethodPut, "/api/usage/limits", strings.NewReader(`{"kind":"workspace"}`))
	req.Header.Set("X-User", "018f3b2c-7a10-7c4a-8e11-111111111111")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("omitted amount PUT = %d, want 422", rec.Code)
	}

	// Non-integer amount_micro_usd -> 400.
	req = httptest.NewRequest(http.MethodPut, "/api/usage/limits", strings.NewReader(`{"kind":"workspace","amount_micro_usd":12.5}`))
	req.Header.Set("X-User", "018f3b2c-7a10-7c4a-8e11-111111111111")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("float amount PUT = %d, want 400", rec.Code)
	}

	// Oversized body -> 413.
	req = httptest.NewRequest(http.MethodPut, "/api/usage/limits", strings.NewReader(`{"kind":"`+strings.Repeat("a", maxLimitBody+10)+`","amount_micro_usd":1}`))
	req.Header.Set("X-User", "018f3b2c-7a10-7c4a-8e11-111111111111")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized PUT = %d, want 413", rec.Code)
	}

	if store.setCalls != 0 {
		t.Fatalf("store.setCalls = %d, want 0", store.setCalls)
	}
}
