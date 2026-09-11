package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var ErrReview = errors.New("tasks: review")

// Review is the only text a clean-context reviewer sees: the criteria and the diff.
func Review(diff, criteria string) (string, error) {
	diff = strings.TrimSpace(diff)
	criteria = strings.TrimSpace(criteria)
	if diff == "" || criteria == "" ||
		utf8.RuneCountInString(diff) > 8000 ||
		utf8.RuneCountInString(criteria) > 8000 {
		return "", ErrReview
	}

	return "criteria:\n" + criteria + "\n\ndiff:\n" + diff + "\n", nil
}
