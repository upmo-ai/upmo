package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	// ProfileBytes is the profile budget: 300 tokens at four bytes each.
	ProfileBytes = 300 * 4
	// LineRunes is the longest line of chat.
	LineRunes = 4000
)

var (
	// ErrProfile is a profile the cache prefix cannot hold.
	ErrProfile = errors.New("chat: profile")
	// ErrLine is a line that is empty or too long.
	ErrLine = errors.New("chat: line")
	// ErrWriter is a change to a line after it was stored.
	ErrWriter = errors.New("chat: writer")
)

// Profile trims the profile and refuses one past the prefix budget.
// An empty profile is a chat with no block.
func Profile(body string) (string, error) {
	body = strings.TrimSpace(body)
	if len(body) > ProfileBytes {
		return "", ErrProfile
	}
	return body, nil
}

// Block is the cacheable prefix. Facts do not go here.
func Block(profile string) string {
	if profile == "" {
		return ""
	}
	return "Profile:\n" + profile
}

// Line trims one line of chat.
func Line(body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" || utf8.RuneCountInString(body) > LineRunes {
		return "", ErrLine
	}
	return body, nil
}
