package domain

import (
	"strings"
	"unicode/utf8"
)

const CheckRunes = 80

type Check struct {
	Name string
	Code int
}

// Passes is true only when the core recorded at least one check and every check exited 0.
func Passes(checks []Check) bool {
	if len(checks) == 0 {
		return false
	}

	for _, check := range checks {
		name := strings.TrimSpace(check.Name)
		if name == "" || utf8.RuneCountInString(name) > CheckRunes || check.Code != 0 {
			return false
		}
	}

	return true
}
