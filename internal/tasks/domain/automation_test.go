package domain

import (
	"errors"
	"testing"
)

func TestTriggerAcceptsTheFourKinds(t *testing.T) {
	for _, kind := range []string{KindCron, KindEvent, KindWebhook, KindChannel} {
		got, err := Trigger(kind, "  0 9 * * 1  ")
		if err != nil || got != "0 9 * * 1" {
			t.Fatalf("%s: %q %v", kind, got, err)
		}
	}
	if _, err := Trigger("manual", "daily"); !errors.Is(err, ErrInput) {
		t.Fatalf("kind error = %v", err)
	}
}

func TestFillReplacesEverySlot(t *testing.T) {
	got, err := Fill("File the {{kind}} for {{client}}", map[string]string{
		"kind":   "receipts",
		"client": "Ada",
	})
	if err != nil || got != "File the receipts for Ada" {
		t.Fatalf("fill = %q %v", got, err)
	}
	if _, err := Fill("File the {{kind}}", map[string]string{}); !errors.Is(err, ErrSlot) {
		t.Fatalf("missing slot error = %v", err)
	}
	got, err = Fill("File the {{kind}}", map[string]string{"kind": "{ {no}}"})
	if err != nil || got != "File the { {no}}" {
		t.Fatalf("neutralised value = %q %v", got, err)
	}
}

func TestBreakerTripsOnTheThirdFailure(t *testing.T) {
	next, tripped := Breaker(1)
	if next != 2 || tripped {
		t.Fatalf("second = %d %v", next, tripped)
	}
	next, tripped = Breaker(2)
	if next != BreakerLimit || !tripped {
		t.Fatalf("third = %d %v", next, tripped)
	}
}
