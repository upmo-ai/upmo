// Package writeguard applies the untrusted-write rule: when a
// run has read untrusted content, holds private data and has an external
// write tool, every external write becomes ask. The rule is Go, never a
// prompt.
package writeguard

// Verdict is allow or ask. This package never blocks; it only downgrades.
type Verdict string

const (
	Allow Verdict = "allow"
	Ask   Verdict = "ask"
	block         = "block"
)

// Exposure is what a run has already touched and which tools it was given.
type Exposure struct {
	Untrusted bool
	Private   bool
	External  bool
}

// Armed reports whether all three arms are present.
func Armed(x Exposure) bool {
	return x.Untrusted && x.Private && x.External
}

// Write is ask when the trifecta is armed, otherwise allow.
func Write(x Exposure) Verdict {
	if Armed(x) {
		return Ask
	}
	return Allow
}

// Tighten never loosens. Block stays block. Ask stays ask. Allow becomes
// ask when armed.
func Tighten(current string, x Exposure) string {
	if current == block {
		return block
	}
	if Armed(x) {
		return string(Ask)
	}
	if current == string(Ask) {
		return string(Ask)
	}
	return string(Allow)
}
