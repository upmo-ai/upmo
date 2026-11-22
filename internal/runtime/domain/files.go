package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	// ReadBudget is the most bytes a file tool may read in one range.
	ReadBudget = 32768
	// OutlineCap is the most headings an outline may keep.
	OutlineCap = 64
)

// headingRunes is the longest a single outline heading may be, in runes.
const headingRunes = 200

// hashLength is the length of a hexadecimal SHA-256 digest.
const hashLength = 64

// ErrFile reports an outline, range or hash that a file tool refuses.
var ErrFile = errors.New("runtime: file")

// Outline trims each heading, drops blanks and keeps the rest in order.
// It refuses the whole list when a heading is too long or spans lines,
// or when more than OutlineCap headings remain.
func Outline(headings []string) ([]string, error) {
	kept := make([]string, 0, len(headings))
	for _, heading := range headings {
		heading = strings.TrimSpace(heading)
		if heading == "" {
			continue
		}
		if strings.Contains(heading, "\n") || utf8.RuneCountInString(heading) > headingRunes {
			return nil, ErrFile
		}
		kept = append(kept, heading)
		if len(kept) > OutlineCap {
			return nil, ErrFile
		}
	}
	return kept, nil
}

// AdmitRange refuses a byte range that is negative, empty, reversed or longer than ReadBudget.
func AdmitRange(start, end int) error {
	if start < 0 || end <= start || end-start > ReadBudget {
		return ErrFile
	}
	return nil
}

// Seen reports whether hash is among known. It compares normalized digests,
// refuses a hash that is not 64 hexadecimal characters and skips any known
// entry that is not one either.
func Seen(hash string, known []string) (bool, error) {
	hash, ok := normalizeHash(hash)
	if !ok {
		return false, ErrFile
	}
	for _, entry := range known {
		entry, ok := normalizeHash(entry)
		if ok && entry == hash {
			return true, nil
		}
	}
	return false, nil
}

// normalizeHash trims and lowercases a digest and reports whether it is 64 hexadecimal characters.
func normalizeHash(hash string) (string, bool) {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if len(hash) != hashLength {
		return "", false
	}
	for i := 0; i < len(hash); i++ {
		c := hash[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", false
		}
	}
	return hash, true
}
