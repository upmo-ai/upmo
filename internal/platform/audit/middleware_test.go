package audit

import (
	"io"
	"log/slog"
	"testing"
)

func TestEventForPicksTheOutcome(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	route := Route{Action: AuthSignOut}

	tests := []struct {
		name   string
		status int
		want   Outcome
		reason string
	}{
		{"unauthorised", 401, Denied, ""},
		{"forbidden", 403, Denied, ""},
		{"throttled", 429, Denied, ""},
		{"broken", 500, Failure, ""},
		{"unrecorded", 204, Success, "unrecorded"},
		{"redirect without event", 302, Success, "unrecorded"},
		{"bad request", 400, "", ""},
		{"not found", 404, "", ""},
		{"conflict", 409, "", ""},
		{"unprocessable", 422, "", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := &scope{}
			event, write := eventFor(route, test.status, s, log)
			if test.want == "" {
				if write {
					t.Fatalf("status %d wrote an event", test.status)
				}
				return
			}
			if !write {
				t.Fatalf("status %d wrote nothing, want %s", test.status, test.want)
			}
			if event.Outcome != test.want || event.Reason != test.reason {
				t.Fatalf("event = %s %q, want %s %q", event.Outcome, event.Reason, test.want, test.reason)
			}
		})
	}
}

func TestEventForHonoursRecordedEvents(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	route := Route{Action: AuthSignOut}

	recorded := &scope{}
	recorded.recorded = true
	if _, write := eventFor(route, 204, recorded, log); write {
		t.Fatal("a route that recorded its own event got a fallback")
	}

	failed := &scope{}
	failed.recorded = true
	if _, write := eventFor(route, 500, failed, log); !write {
		t.Fatal("a failed response needs its own failure event")
	}
}

func TestEventForMergesTheAnnotation(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	route := Route{Action: AuthSignOut}
	s := &scope{annotated: Event{Reason: "cross-origin", TargetType: "session", TargetID: "s1"}}

	event, write := eventFor(route, 403, s, log)
	if !write || event.Outcome != Denied {
		t.Fatalf("event = %v, want a denial", event)
	}
	if event.Reason != "cross-origin" || event.TargetType != "session" || event.TargetID != "s1" {
		t.Fatalf("annotation lost: %v", event)
	}
}

func TestUnrecordedHookFires(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	route := Route{Action: AuthSignOut}
	fired := Action("")
	previous := onUnrecorded
	onUnrecorded = func(action Action) { fired = action }
	t.Cleanup(func() { onUnrecorded = previous })

	s := &scope{}
	event, write := eventFor(route, 200, s, log)
	if !write || event.Reason != "unrecorded" {
		t.Fatalf("event = %v, want the unrecorded fallback", event)
	}
	if fired != AuthSignOut {
		t.Fatalf("hook saw %s, want %s", fired, AuthSignOut)
	}
}
