package domain

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// PinModelRunes is the longest model id a pin may name.
const PinModelRunes = 120

var (
	// ErrPin is a pin that is half given, whose account is not a UUID, or
	// whose model is empty, too long or holds a control character.
	ErrPin = errors.New("chat: pin")
	// ErrAgent is an agent id that is not a UUID.
	ErrAgent = errors.New("chat: agent")
)

// Pin checks the model a person pinned for a line: the provider account and
// the model, both or neither. The account comes back in its canonical form.
func Pin(account, model string) (string, string, error) {
	account, model = strings.TrimSpace(account), strings.TrimSpace(model)
	if account == "" && model == "" {
		return "", "", nil
	}
	id, err := uuid.Parse(account)
	if err != nil || model == "" || utf8.RuneCountInString(model) > PinModelRunes ||
		!utf8.ValidString(model) || strings.ContainsFunc(model, unicode.IsControl) {
		return "", "", ErrPin
	}
	return id.String(), model, nil
}

// Agent checks the agent a person picked for a line. Empty is allowed and
// means none.
func Agent(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", nil
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return "", ErrAgent
	}
	return parsed.String(), nil
}
