package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAccountChargedToLimit(t *testing.T) {
	cases := []struct {
		name string
		acc  Account
		want bool
	}{
		{"house metered", Account{PlanKind: PlanMetered, House: true, Visibility: VisibilityEveryone}, true},
		{"team metered", Account{PlanKind: PlanMetered, Visibility: VisibilityTeam}, true},
		{"everyone metered", Account{PlanKind: PlanMetered, Visibility: VisibilityEveryone}, true},
		{"private metered", Account{PlanKind: PlanMetered, Visibility: VisibilityPrivate}, false},
		{"house subscription", Account{PlanKind: PlanSubscription, House: true, Visibility: VisibilityEveryone}, false},
		{"house local", Account{PlanKind: PlanLocal, House: true, Visibility: VisibilityEveryone}, false},
	}
	for _, tc := range cases {
		if got := AccountChargedToLimit(tc.acc); got != tc.want {
			t.Errorf("%s: got %t, want %t", tc.name, got, tc.want)
		}
	}
}

func TestPeriodStartUsesWorkspaceLocation(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-12-01 03:30 UTC is 2026-11-30 22:30 in New York, and 2026-12-01 12:30 in Tokyo.
	at := time.Date(2026, 12, 1, 3, 30, 0, 0, time.UTC)
	if got := PeriodStart(at, ny).Format("2006-01-02"); got != "2026-11-01" {
		t.Fatalf("New York period = %s, want 2026-11-01", got)
	}
	if got := PeriodStart(at, tokyo).Format("2006-01-02"); got != "2026-12-01" {
		t.Fatalf("Tokyo period = %s, want 2026-12-01", got)
	}
	if got := PeriodStart(at, nil).Format("2006-01-02"); got != "2026-12-01" {
		t.Fatalf("nil location period = %s, want 2026-12-01", got)
	}
}

func TestEffectiveLimits(t *testing.T) {
	p := func(v int64) *int64 { return &v }
	if got := EffectiveWorkspaceLimit(nil, nil); got != nil {
		t.Fatalf("nil/nil = %d, want nil", *got)
	}
	if got := EffectiveWorkspaceLimit(p(500), nil); got == nil || *got != 500 {
		t.Fatalf("500/nil = %v, want 500", got)
	}
	if got := EffectiveWorkspaceLimit(nil, p(300)); got == nil || *got != 300 {
		t.Fatalf("nil/300 = %v, want 300", got)
	}
	if got := EffectiveWorkspaceLimit(p(500), p(300)); got == nil || *got != 300 {
		t.Fatalf("500/300 = %v, want 300", got)
	}
	if got := EffectiveWorkspaceLimit(p(200), p(300)); got == nil || *got != 200 {
		t.Fatalf("200/300 = %v, want 200", got)
	}

	if got := EffectivePersonLimit(nil, nil); got != nil {
		t.Fatalf("person nil/nil = %d, want nil", *got)
	}
	if got := EffectivePersonLimit(nil, p(400)); got == nil || *got != 400 {
		t.Fatalf("person nil/400 = %v, want 400", got)
	}
	if got := EffectivePersonLimit(p(900), p(400)); got == nil || *got != 900 {
		t.Fatalf("person 900/400 = %v, want 900", got)
	}
}

func TestReserveEstimate(t *testing.T) {
	// 5 input bytes -> ceil(5/4) = 2 tokens; + 10 max output = 12 tokens at $10/MTok = 120 micro-USD.
	if got := ReserveEstimate(PlanMetered, 10_000_000, 5, 10); got != 120 {
		t.Fatalf("ReserveEstimate = %d, want 120", got)
	}
	// Minimum 1 token at fallback rate ($75/MTok = 75 micro-USD/token).
	if got := ReserveEstimate(PlanMetered, 0, 0, 0); got != 75 {
		t.Fatalf("ReserveEstimate empty = %d, want 75", got)
	}
	if got := ReserveEstimate(PlanLocal, 10_000_000, 100, 100); got != 0 {
		t.Fatalf("ReserveEstimate local = %d, want 0", got)
	}
}

func TestCheckLimit(t *testing.T) {
	p := func(v int64) *int64 { return &v }
	if err := CheckLimit(LimitWorkspace, nil, 1000, 500, 100); err != nil {
		t.Fatalf("nil limit failed: %v", err)
	}
	if err := CheckLimit(LimitPerson, p(100), 60, 20, 20); err != nil {
		t.Fatalf("exact boundary failed: %v", err)
	}
	err := CheckLimit(LimitPerson, p(100), 60, 20, 21)
	var reached LimitReached
	if !errors.As(err, &reached) {
		t.Fatalf("err = %v, want LimitReached", err)
	}
	if reached.Kind != LimitPerson || reached.Limit != 100 || reached.Spent != 60 || reached.Held != 20 {
		t.Fatalf("reached = %+v", reached)
	}
	if !strings.Contains(err.Error(), LimitPerson) {
		t.Fatalf("error %q does not name limit %q", err.Error(), LimitPerson)
	}
	if err := CheckLimit(LimitWorkspace, p(100), 60, 20, 21); err == nil || strings.Contains(err.Error(), "100") || strings.Contains(err.Error(), "80") || strings.Contains(err.Error(), "60") {
		t.Fatalf("workspace error %v leaked spend amounts", err)
	}
	if err := CheckLimit(LimitAutomation, p(0), 0, 0, 1); !errors.As(err, &reached) || reached.Kind != LimitAutomation {
		t.Fatalf("zero limit = %v, want LimitReached automation", err)
	}
	if err := CheckLimit(LimitWorkspace, p(100), 120, 0, 1); !errors.As(err, &reached) || reached.Kind != LimitWorkspace {
		t.Fatalf("overshot spent = %v, want LimitReached workspace", err)
	}
}
