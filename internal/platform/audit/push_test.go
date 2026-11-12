package audit

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

// One event frames as one RFC 5424 message in an RFC 6587 octet-counted
// frame: the event's own time and id in the header — the id the uid without
// its hyphens, 32 characters of a UUID — the OCSF JSON as the message, as is.
func TestSyslogFrame(t *testing.T) {
	event, err := os.ReadFile("testdata/ocsf.golden") //nolint:gosec // a fixed path in this package
	if err != nil {
		t.Fatal(err)
	}
	const hostname = "siem-test"
	message := "<110>1 2026-11-11T10:30:00.123Z " + hostname +
		" upmo - 0199a2f57a017c1e9b3a5c2d8e4f6a01 - " + string(event)
	want := strconv.Itoa(len(message)) + " " + message
	if got := string(syslogFrame(hostname, event)); got != want {
		t.Fatalf("frame:\n got %s\nwant %s", got, want)
	}
}

// An event without a time or id of its own, and a host without a name, still
// frame: those header fields fall back to the nil value.
func TestSyslogFrameWithoutTheEvent(t *testing.T) {
	if got, want := string(syslogFrame("siem-test", json.RawMessage(`{}`))), "32 <110>1 - siem-test upmo - - - {}"; got != want {
		t.Fatalf("frame:\n got %s\nwant %s", got, want)
	}
	if got, want := string(syslogFrame("", json.RawMessage(`{}`))), "24 <110>1 - - upmo - - - {}"; got != want {
		t.Fatalf("frame:\n got %s\nwant %s", got, want)
	}
}

// A hostname HOSTNAME cannot hold — one with a space, one outside printable
// ASCII, one past 255 characters — becomes the nil value, keeping the header
// legal RFC 5424.
func TestSyslogFrameWithAHostnameTheHeaderCannotHold(t *testing.T) {
	for _, hostname := range []string{"two words", "siém", "tab\tname", strings.Repeat("h", 256)} {
		if got, want := string(syslogFrame(hostname, json.RawMessage(`{}`))), "24 <110>1 - - upmo - - - {}"; got != want {
			t.Fatalf("hostname %q:\n got %s\nwant %s", hostname, got, want)
		}
	}
}
