package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	KindCron        = "cron"
	KindEvent       = "event"
	KindWebhook     = "webhook"
	KindChannel     = "channel"
	ModeOn          = "on"
	ModeOff         = "off"
	ReasonBreaker   = "breaker"
	ReasonOwnerLeft = "owner_left"
	// BreakerLimit is the number of failed fires that turns an automation off.
	BreakerLimit = 3
	// SpecRunes is the longest trigger spec or dedup key.
	SpecRunes = 200
)

var (
	// ErrMode is an automation that is off, or a mode the caller cannot set.
	ErrMode = errors.New("tasks: mode")
	// ErrSelf is a fire whose source task was opened by the same automation.
	ErrSelf = errors.New("tasks: self")
)

// Trigger checks a trigger kind and returns the trimmed spec.
func Trigger(kind, spec string) (string, error) {
	switch kind {
	case KindCron, KindEvent, KindWebhook, KindChannel:
	default:
		return "", ErrInput
	}
	spec = strings.TrimSpace(spec)
	if spec == "" || utf8.RuneCountInString(spec) > SpecRunes {
		return "", ErrInput
	}
	return spec, nil
}

// DedupKey trims the key that makes one fire distinct.
func DedupKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" || utf8.RuneCountInString(key) > SpecRunes {
		return "", ErrInput
	}
	return key, nil
}

// Fill replaces each slot in the goal. Every slot needs a value.
// The result must still fit in a task goal and must not contain a slot.
func Fill(goal string, values map[string]string) (string, error) {
	goal = strings.TrimSpace(goal)
	names, err := templateSlots(goal)
	if err != nil {
		return "", err
	}
	for _, name := range names {
		value := strings.TrimSpace(values[name])
		if value == "" || utf8.RuneCountInString(value) > SpecRunes || strings.Contains(value, "{{") {
			return "", ErrSlot
		}
		goal = strings.ReplaceAll(goal, "{{"+name+"}}", value)
	}
	if strings.Contains(goal, "{{") {
		return "", ErrSlot
	}
	if goal == "" || utf8.RuneCountInString(goal) > GoalRunes {
		return "", ErrGoal
	}
	return goal, nil
}

// Breaker reports the failure count after one more failure, and whether it trips.
func Breaker(failures int) (int, bool) {
	next := failures + 1
	if next >= BreakerLimit {
		return BreakerLimit, true
	}
	return next, false
}
