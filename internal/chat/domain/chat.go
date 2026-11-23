package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// LineRunes is the longest line of chat.
const LineRunes = 32000

// TitleRunes is the longest title a chat shows in a list.
const TitleRunes = 60

// PreviewRunes is the longest preview a chat shows in a list.
const PreviewRunes = 120

// NewChatTitle is the title of a chat without a user line.
const NewChatTitle = "New chat"

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

// Title names a chat after its first user line. A chat without one is new.
func Title(firstUserLine string) string {
	if strings.TrimSpace(firstUserLine) == "" {
		return NewChatTitle
	}
	return cut(firstUserLine, TitleRunes)
}

// Preview carries a chat's newest line into a list.
func Preview(newestLine string) string {
	return cut(newestLine, PreviewRunes)
}

// cut flattens each run of whitespace to one space, trims, and keeps at most
// limit runes. Nothing is added at the cut.
func cut(body string, limit int) string {
	flat := []rune(strings.Join(strings.Fields(body), " "))
	if len(flat) > limit {
		flat = flat[:limit]
	}
	return strings.TrimRight(string(flat), " ")
}
