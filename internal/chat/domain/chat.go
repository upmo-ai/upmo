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

// QueryRunes is the longest search query a chat accepts.
const QueryRunes = 200

// PreviewRunes is the longest preview a chat shows in a list.
const PreviewRunes = 120

// NewChatTitle is the title of a chat without a user line.
const NewChatTitle = "New chat"

var (
	// ErrLine is a line that is empty or too long.
	ErrLine = errors.New("chat: line")
	// ErrTitle is a title that is empty or longer than TitleRunes.
	ErrTitle = errors.New("chat: title")
	// ErrQuery is a search query longer than QueryRunes.
	ErrQuery = errors.New("chat: query")
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

// CustomTitle validates a user-chosen chat title (1..TitleRunes runes,
// flattened whitespace).
func CustomTitle(raw string) (string, error) {
	flat := strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
	n := utf8.RuneCountInString(flat)
	if n < 1 || n > TitleRunes {
		return "", ErrTitle
	}
	return flat, nil
}

// DisplayTitle names a chat: its custom title when it has one, else the
// derived title of its first user line.
func DisplayTitle(customTitle, firstUserLine string) string {
	if strings.TrimSpace(customTitle) != "" {
		return cut(customTitle, TitleRunes)
	}
	return Title(firstUserLine)
}

// Preview carries a chat's newest line into a list.
func Preview(newestLine string) string {
	return cut(newestLine, PreviewRunes)
}

// SearchQuery validates and trims an optional search query (0..QueryRunes runes).
func SearchQuery(raw string) (string, error) {
	q := strings.TrimSpace(raw)
	if utf8.RuneCountInString(q) > QueryRunes {
		return "", ErrQuery
	}
	return q, nil
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
