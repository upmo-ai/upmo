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

// Chat is one owned chat. Block is the cacheable prefix, not a line.
type Chat struct {
	ID      string
	Profile string
	Block   string
	Lines   []string
}

// Book stores chats and their lines.
type Book interface {
	Open(ctx context.Context, access Access, profile string) (string, error)
	Say(ctx context.Context, access Access, chatID, body string) error
	Read(ctx context.Context, access Access, chatID string) (Chat, error)
}

// Open starts a chat for this person and stores the profile block.
// It does not open a login session.
func Open(ctx context.Context, book Book, access Access, profile string) (string, error) {
	if err := caller(access); err != nil {
		return "", err
	}
	profile, err := domain.Profile(strings.TrimSpace(trustfence.NeutralizeSlot(profile)))
	if err != nil {
		return "", err
	}
	return book.Open(ctx, access, profile)
}

// Say appends one line. The profile block stays as it was at open.
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

// Read returns the chat, with the profile rendered as the prefix.
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
	got.Block = domain.Block(got.Profile)
	return got, nil
}

func caller(access Access) error {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return ErrAccess
	}
	return nil
}
