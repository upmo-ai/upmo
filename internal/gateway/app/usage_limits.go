package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// Usage limit errors surfaced by the HTTP layer with fixed plain messages.
var (
	ErrLimitForbidden   = errors.New("gateway: changing spend limits needs settings:manage")
	ErrCeilingForbidden = domain.ErrCeilingLimit
)

// OwnUsageLimits holds the caller's effective monthly limits and current-period
// spend counters.
type OwnUsageLimits struct {
	LimitMicroUSD          *int64 `json:"limit_micro_usd"`
	WorkspaceLimitMicroUSD *int64 `json:"workspace_limit_micro_usd"`
	SpentMicroUSD          int64  `json:"spent_micro_usd"`
	HeldMicroUSD           int64  `json:"held_micro_usd"`
}

// UsageLimitRow describes one limit or current-period spend entry returned to
// callers holding usage:view.
type UsageLimitRow struct {
	Kind           string     `json:"kind"`
	SubjectID      string     `json:"subject_id,omitempty"`
	AmountMicroUSD *int64     `json:"amount_micro_usd"`
	SpentMicroUSD  int64      `json:"spent_micro_usd"`
	HeldMicroUSD   int64      `json:"held_micro_usd"`
	UpdatedAt      *time.Time `json:"updated_at,omitempty"`
}

// UsageLimitsView is the response payload for GET /api/usage/limits.
// Rows is non-nil only when the caller holds usage:view.
type UsageLimitsView struct {
	PeriodStart string           `json:"period_start"`
	Own         OwnUsageLimits   `json:"own"`
	Rows        *[]UsageLimitRow `json:"rows,omitempty"`
}

// SetUsageLimit names one monthly limit change. A nil AmountMicroUSD removes
// the limit row.
type SetUsageLimit struct {
	Kind           string
	SubjectID      string
	AmountMicroUSD *int64
}

// UpdatedUsageLimit is the response payload for PUT /api/usage/limits.
type UpdatedUsageLimit struct {
	Kind           string     `json:"kind"`
	SubjectID      string     `json:"subject_id,omitempty"`
	AmountMicroUSD *int64     `json:"amount_micro_usd"`
	UpdatedAt      *time.Time `json:"updated_at,omitempty"`
}

// UsageLimitStore reads and writes gateway.usage_limits and reads
// gateway.usage_spend under row security.
type UsageLimitStore interface {
	GetUsageLimits(ctx context.Context, who Caller, periodStart time.Time, includeAll bool) (UsageLimitsView, error)
	SetUsageLimit(ctx context.Context, who Caller, kind, subjectID string, amount *int64) (UpdatedUsageLimit, error)
}

// UsageLimits serves reading and managing monthly spend limits.
type UsageLimits struct {
	Store            UsageLimitStore
	Allow            func(ctx context.Context, user string) (string, error)
	TimeZone         func(ctx context.Context) (string, error)
	PersonExists     func(ctx context.Context, id string) (bool, error)
	AutomationExists func(ctx context.Context, id string) (bool, error)
	Now              func() time.Time
	Log              *slog.Logger
}

func (s UsageLimits) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s UsageLimits) caller(ctx context.Context, user string) (Caller, error) {
	who := Caller{User: user}
	if s.Allow != nil {
		allow, err := s.Allow(ctx, user)
		if err != nil {
			return Caller{}, fmt.Errorf("gateway: permissions: %w", err)
		}
		who.Allow = allow
	}
	return who, nil
}

// Get returns the caller's own limits and current-period spend, plus every
// limit and current-period spend row when the caller holds usage:view.
func (s UsageLimits) Get(ctx context.Context, user string) (UsageLimitsView, error) {
	if s.Store == nil {
		return UsageLimitsView{}, fmt.Errorf("gateway: no usage limit store")
	}
	who, err := s.caller(ctx, user)
	if err != nil {
		return UsageLimitsView{}, err
	}
	periodStart, err := resolvePeriodStart(ctx, s.TimeZone, s.now())
	if err != nil {
		return UsageLimitsView{}, err
	}
	return s.Store.GetUsageLimits(ctx, who, periodStart, who.mayViewUsage())
}

// Set creates, updates, or removes one monthly limit when the caller holds
// settings:manage.
func (s UsageLimits) Set(ctx context.Context, user string, in SetUsageLimit) (UpdatedUsageLimit, error) {
	if s.Store == nil {
		return UpdatedUsageLimit{}, fmt.Errorf("gateway: no usage limit store")
	}
	who, err := s.caller(ctx, user)
	if err != nil {
		return UpdatedUsageLimit{}, err
	}
	if !who.mayShare() {
		return UpdatedUsageLimit{}, ErrLimitForbidden
	}
	kind, subjectID, amount, err := domain.CleanLimitInput(in.Kind, in.SubjectID, in.AmountMicroUSD)
	if err != nil {
		return UpdatedUsageLimit{}, err
	}
	switch kind {
	case domain.LimitPerson:
		if s.PersonExists == nil {
			return UpdatedUsageLimit{}, fmt.Errorf("gateway: person lookup not configured")
		}
		ok, err := s.PersonExists(ctx, subjectID)
		if err != nil {
			return UpdatedUsageLimit{}, fmt.Errorf("gateway: check person: %w", err)
		}
		if !ok {
			return UpdatedUsageLimit{}, domain.Invalid("Unknown person.")
		}
	case domain.LimitAutomation:
		if s.AutomationExists == nil {
			return UpdatedUsageLimit{}, fmt.Errorf("gateway: automation lookup not configured")
		}
		ok, err := s.AutomationExists(ctx, subjectID)
		if err != nil {
			return UpdatedUsageLimit{}, fmt.Errorf("gateway: check automation: %w", err)
		}
		if !ok {
			return UpdatedUsageLimit{}, domain.Invalid("Unknown automation.")
		}
	}
	return s.Store.SetUsageLimit(ctx, who, kind, subjectID, amount)
}
