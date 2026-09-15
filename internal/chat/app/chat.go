package app

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/platform/trustfence"
)

var (
	// ErrAccess is a missing person or an empty allow list.
	ErrAccess = errors.New("chat: access")
	// ErrChat is a chat id that is not a UUID.
	ErrChat = errors.New("chat: chat")
	// ErrGone is a chat this person cannot see.
	ErrGone = errors.New("chat: no such chat")
)

// Access is the signed-in person and the allow list on this call.
type Access struct {
	UserID string
	Allow  string
}

// Chat is one owned chat and the lines said in it.
type Chat struct {
	ID    string
	Lines []string
}

// Book stores chats and their lines.
type Book interface {
	Open(ctx context.Context, access Access) (string, error)
	Say(ctx context.Context, access Access, chatID, body string) error
	Read(ctx context.Context, access Access, chatID string) (Chat, error)
}

// Open starts a chat for this person. The profile stays in memory and is read when a turn is built.
// It does not open a login session.
func Open(ctx context.Context, book Book, access Access) (string, error) {
	if err := caller(access); err != nil {
		return "", err
	}
	return book.Open(ctx, access)
}

// Say appends one line.
func Say(ctx context.Context, book Book, access Access, chatID, body string) error {
	if err := caller(access); err != nil {
		return err
	}
	if _, err := uuid.Parse(chatID); err != nil {
		return ErrChat
	}
	body, err := domain.Line(strings.TrimSpace(trustfence.NeutralizeSlot(body)))
	if err != nil {
		return err
	}
	return book.Say(ctx, access, chatID, body)
}

// Read returns the chat and its lines in order.
func Read(ctx context.Context, book Book, access Access, chatID string) (Chat, error) {
	if err := caller(access); err != nil {
		return Chat{}, err
	}
	if _, err := uuid.Parse(chatID); err != nil {
		return Chat{}, ErrChat
	}
	got, err := book.Read(ctx, access, chatID)
	if err != nil {
		return Chat{}, err
	}
	if got.Lines == nil {
		got.Lines = []string{}
	}
	return got, nil
}

func caller(access Access) error {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return ErrAccess
	}
	return nil
}
