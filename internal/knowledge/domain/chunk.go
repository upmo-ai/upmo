package domain

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// MinTokens and MaxTokens bound a chunk of a long source. A short source
	// stays one chunk, because the text is not padded or merged.
	MinTokens = 400
	MaxTokens = 600
	// TargetTokens is the size the cutter aims for inside that range.
	TargetTokens = 500
	// TokenBytes is the profile budget's unit: four bytes are one token.
	TokenBytes = 4
	// OverlapPercent is how much of the target the next chunk repeats.
	OverlapPercent = 15
)

// ErrEmpty is a source with no text to store.
var ErrEmpty = errors.New("knowledge: empty source")

// Chunk slices source into verbatim pieces. Every piece is a substring.
// Nothing is rewritten or joined into a new sentence.
func Chunk(source string) ([]string, error) {
	if strings.TrimSpace(source) == "" {
		return nil, ErrEmpty
	}
	maxBytes := MaxTokens * TokenBytes
	if len(source) <= maxBytes {
		return []string{source}, nil
	}
	target := TargetTokens * TokenBytes
	minBytes := MinTokens * TokenBytes
	overlap := target * OverlapPercent / 100

	var out []string
	start := 0
	for start < len(source) {
		rest := len(source) - start
		var end, next int
		if rest <= maxBytes {
			end = len(source)
			next = len(source)
		} else {
			end = nearestBoundary(source, start+target, start+minBytes, start+maxBytes)
			if end <= start {
				return nil, fmt.Errorf("knowledge: chunk did not advance")
			}
			next = nearestBoundary(source, end-overlap, start+1, end-1)
			if next <= start || next >= end {
				next = end
			}
		}
		out = append(out, source[start:end])
		if next <= start {
			return nil, fmt.Errorf("knowledge: chunk did not advance")
		}
		start = next
	}
	return out, nil
}

// nearestBoundary picks the cut closest to want inside [lo, hi]. A cut is
// the start of a rune, and it follows a space or a newline. hi may be len(s).
func nearestBoundary(s string, want, lo, hi int) int {
	if hi > len(s) {
		hi = len(s)
	}
	if lo < 0 {
		lo = 0
	}
	if lo > hi {
		lo = hi
	}
	if want < lo {
		want = lo
	}
	if want > hi {
		want = hi
	}
	best := -1
	bestDist := len(s) + 1
	for i := lo; i <= hi; i++ {
		if !cutAt(s, i) {
			continue
		}
		dist := want - i
		if dist < 0 {
			dist = -dist
		}
		if dist < bestDist {
			bestDist = dist
			best = i
		}
	}
	if best >= 0 {
		return best
	}
	return align(s, want, hi)
}

func cutAt(s string, i int) bool {
	if i <= 0 || i > len(s) {
		return false
	}
	if i < len(s) && !utf8.RuneStart(s[i]) {
		return false
	}
	return s[i-1] == ' ' || s[i-1] == '\n'
}

func align(s string, i, hi int) int {
	if i < 0 {
		i = 0
	}
	if hi > len(s) {
		hi = len(s)
	}
	if i > hi {
		i = hi
	}
	for i < hi && i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return i
}
