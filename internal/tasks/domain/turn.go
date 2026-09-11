package domain

import (
	"errors"
	"strings"
)

const Sentinel = "upmo:end"

var ErrTurn = errors.New("tasks: turn")

// EndTurn reports whether the turn ended on the sentinel line.
// body is the text with that line removed. A claim such as "pass" or "done" does not end a turn.
func EndTurn(text string) (body string, ended bool) {
	trimmed := strings.TrimSpace(text)
	lines := strings.Split(trimmed, "\n")
	for index := range lines {
		lines[index] = strings.TrimSpace(lines[index])
	}

	if lines[len(lines)-1] != Sentinel {
		return trimmed, false
	}

	return strings.Join(lines[:len(lines)-1], "\n"), true
}
