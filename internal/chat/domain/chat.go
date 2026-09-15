package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// LineRunes is the longest line of chat.
const LineRunes = 4000

var (
	// ErrLine is a line that is empty or too long.
	ErrLine = errors.New("chat: line")
	// ErrWriter is a change to a line after it was stored.
	ErrWriter = errors.New("chat: writer")
)

// Line trims one line of chat.
func Line(body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" || utf8.RuneCountInString(body) > LineRunes {
		return "", ErrLine
	}
	return body, nil
}
