package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

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
	// ErrSay is a chat this person can see but not write to.
	ErrSay = errors.New("chat: say")
	// ErrNoLine is a line that is not in the chat.
	ErrNoLine = errors.New("chat: no such line")
	// ErrRetry is a line that is not a failed reply, or one that a later
	// reply has already answered.
	ErrRetry = errors.New("chat: retry")
	// ErrBusy is a line with a reply already on its way.
	ErrBusy = errors.New("chat: reply under way")
	// ErrReplies is a chat that gets no replies in this install.
	ErrReplies = errors.New("chat: replies are off")
)

// Access is the signed-in person and the allow list on this call.
// Teams, SharedView and SharedEdit are uuid array literals; empty is "{}".
type Access struct {
	UserID     string
	Allow      string
	Teams      string
	SharedView string
	SharedEdit string
}

// Line is one stored line of a chat. A user line carries no model and no cost.
// An assistant line is done or failed, says which user line it answers, and
// when failed lists the models tried and why each failed.
type Line struct {
	ID           string
	Role         string
	Body         string
	Model        *string
	Tokens       int
	CostMicroUSD *int64
	At           time.Time
	Status       string
	ReplyTo      *string
	Attempts     []domain.Attempt
}

// ChatSummary is one chat in a list: its derived title and preview, and when
// it was last active.
type ChatSummary struct {
	ID           string
	Title        string
	Preview      string
	LastActivity time.Time
}

// Chat is one owned chat and its lines, oldest first.
type Chat struct {
	ID    string
	Lines []Line
}

// Book stores chats and their lines.
type Book interface {
	Open(ctx context.Context, access Access) (ChatSummary, error)
	List(ctx context.Context, access Access) ([]ChatSummary, error)
	Say(ctx context.Context, access Access, chatID, body, route string) error
	Retry(ctx context.Context, access Access, chatID, lineID, route string) error
	Read(ctx context.Context, access Access, chatID string) (Chat, error)
}

// Open starts a chat for this person. The profile stays in memory and is read when a turn is built.
// It does not open a login session.
func Open(ctx context.Context, book Book, access Access) (ChatSummary, error) {
	if err := caller(access); err != nil {
		return ChatSummary{}, err
	}
	// The database would refuse the insert for a person without chat:create
	// and the handler would answer 500; refuse here so it is a 403.
	if !slices.Contains(strings.Split(access.Allow, ","), "chat:create") {
		return ChatSummary{}, ErrAccess
	}
	return book.Open(ctx, access)
}

// List returns the chats this person can view, newest activity first.
func List(ctx context.Context, book Book, access Access) ([]ChatSummary, error) {
	if err := caller(access); err != nil {
		return nil, err
	}
	got, err := book.List(ctx, access)
	if err != nil {
		return nil, err
	}
	if got == nil {
		got = []ChatSummary{}
	}
	return got, nil
}

// Say appends one line. route names the gateway route that answers it; empty
// takes the install's chat route.
func Say(ctx context.Context, book Book, access Access, chatID, body, route string) error {
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
	route, err = domain.Route(route)
	if err != nil {
		return err
	}
	return book.Say(ctx, access, chatID, body, route)
}

// Retry asks again for the reply to the user line that a failed assistant
// line left unanswered. route is the one picked now; empty takes the
// install's chat route.
func Retry(ctx context.Context, book Book, access Access, chatID, lineID, route string) error {
	if err := caller(access); err != nil {
		return err
	}
	if _, err := uuid.Parse(chatID); err != nil {
		return ErrChat
	}
	if _, err := uuid.Parse(lineID); err != nil {
		return ErrNoLine
	}
	route, err := domain.Route(route)
	if err != nil {
		return err
	}
	return book.Retry(ctx, access, chatID, lineID, route)
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
		got.Lines = []Line{}
	}
	return got, nil
}

func caller(access Access) error {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return ErrAccess
	}
	return nil
}
