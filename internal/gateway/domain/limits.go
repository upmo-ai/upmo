package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// UsageViewPermission lets a caller inspect every workspace spend limit and
// spend counter across all people and automations.
const UsageViewPermission = "usage:view"

// ErrCeilingLimit reports an attempt from inside the workspace to change the
// control-plane ceiling.
var ErrCeilingLimit = errors.New("gateway: workspace ceiling cannot be changed")

// Limit kinds stored in gateway.usage_limits and gateway.usage_spend.
const (
	LimitWorkspace     = "workspace"
	LimitPersonDefault = "person_default"
	LimitPerson        = "person"
	LimitAutomation    = "automation"
	LimitCeiling       = "ceiling"
)

// LimitReached reports that a metered attempt would exceed a spend limit.
type LimitReached struct {
	Kind  string
	Limit int64
	Spent int64
	Held  int64
}

func (e LimitReached) Error() string {
	return fmt.Sprintf("gateway: %s monthly spend limit reached", e.Kind)
}

// AccountChargedToLimit reports whether calls on account count toward spend
// limits: metered house accounts and metered accounts shared with a team or
// everyone. Private, subscription, and local accounts never count.
func AccountChargedToLimit(a Account) bool {
	return a.PlanKind == PlanMetered &&
		(a.House || a.Visibility == VisibilityTeam || a.Visibility == VisibilityEveryone)
}

// PeriodStart returns the first calendar day of now's month in loc as a
// date-only UTC midnight timestamp suitable for a Postgres date column.
func PeriodStart(now time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	y, m, _ := now.In(loc).Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
}

// EffectiveWorkspaceLimit returns the lower of workspace and ceiling, or nil
// when neither is set.
func EffectiveWorkspaceLimit(workspace, ceiling *int64) *int64 {
	switch {
	case workspace == nil && ceiling == nil:
		return nil
	case workspace == nil:
		v := *ceiling
		return &v
	case ceiling == nil:
		v := *workspace
		return &v
	default:
		v := min(*workspace, *ceiling)
		return &v
	}
}

// EffectivePersonLimit returns the per-person override when set, otherwise the
// workspace person_default, or nil when neither is set.
func EffectivePersonLimit(person, personDefault *int64) *int64 {
	if person != nil {
		v := *person
		return &v
	}
	if personDefault != nil {
		v := *personDefault
		return &v
	}
	return nil
}

// ReserveEstimate computes the worst-case micro-USD reservation for one
// attempt from inputBytes at estimateRate plus maxOutputTokens when set.
func ReserveEstimate(plan string, estimateRate int64, inputBytes, maxOutputTokens int) int64 {
	if inputBytes < 0 {
		inputBytes = 0
	}
	if maxOutputTokens < 0 {
		maxOutputTokens = 0
	}
	micro, _, _ := MeteredCost(plan, Usage{Output: maxOutputTokens}, Price{}, false, estimateRate, inputBytes, 0, false)
	if micro == nil {
		return 0
	}
	return *micro
}

// CheckLimit verifies that spent + held + estimate stays within limit. A nil
// limit is unlimited and always passes. Negative balances or estimates fail
// closed with an error.
func CheckLimit(kind string, limit *int64, spent, held, estimate int64) error {
	if spent < 0 || held < 0 || estimate < 0 {
		return fmt.Errorf("gateway: invalid spend counter for %s", kind)
	}
	if limit == nil {
		return nil
	}
	if *limit < 0 {
		return fmt.Errorf("gateway: invalid limit for %s", kind)
	}
	if spent > *limit || held > *limit-spent || estimate > *limit-spent-held {
		return LimitReached{
			Kind:  kind,
			Limit: *limit,
			Spent: spent,
			Held:  held,
		}
	}
	return nil
}

// CleanLimitInput validates and normalizes a workspace usage limit write.
// Kind "ceiling" is refused with ErrCeilingLimit; other invalid inputs return
// Invalid. A nil amount removes the limit.
func CleanLimitInput(kind, subjectID string, amount *int64) (string, string, *int64, error) {
	kind = strings.TrimSpace(kind)
	subjectID = strings.TrimSpace(subjectID)
	switch kind {
	case LimitCeiling:
		return "", "", nil, ErrCeilingLimit
	case LimitWorkspace, LimitPersonDefault:
		if subjectID != "" {
			return "", "", nil, Invalid("Workspace and default person limits do not take a subject_id.")
		}
	case LimitPerson, LimitAutomation:
		subjectID = strings.ToLower(subjectID)
		if !accountID.MatchString(subjectID) {
			return "", "", nil, Invalid("Choose a valid subject_id.")
		}
	default:
		return "", "", nil, Invalid("Choose workspace, person_default, person or automation.")
	}
	if amount == nil {
		return kind, subjectID, nil, nil
	}
	if *amount < 0 {
		return "", "", nil, Invalid("Spend limit cannot be negative.")
	}
	v := *amount
	return kind, subjectID, &v, nil
}
