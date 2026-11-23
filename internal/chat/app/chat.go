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
	// ErrBusy is a chat with a reply already running in it.
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

// Picks are what a person chose for one line in the composer: the gateway
// route, the model pinned (a provider account and a model id, both or
// neither) and the agent. Empty means no pick. They are stored on the user
// line, so a retry and the history can show them.
type Picks struct {
	Route      string
	PinAccount string
	PinModel   string
	AgentID    string
}

// None reports that nothing was picked.
func (p Picks) None() bool { return p == Picks{} }

// Line is one stored line of a chat. A user line carries no model and no cost,
// and the picks it was sent with. An assistant line is done or failed, says
// which user line it answers, and when failed lists the models tried and why
// each failed.
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
	// Picks are set on a user line, each only when the person made it.
	Route      *string
	PinAccount *string
	PinModel   *string
	AgentID    *string
	// Pending is set on a user line that has no stored reply yet, done or failed.
	Pending bool
	Turns   []Turn
}

type Turn struct {
	Seq                          int
	Role, Body, ToolName, ToolID string
	ToolCalls                    []byte
	IsError, Offloaded, Tainted  bool
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
	// Tainted and PrivateRead are what the chat has read so far: untrusted
	// text, and another person's private data. They only turn on.
	Tainted     bool
	PrivateRead bool
}

// Book stores chats and their lines.
type Book interface {
	Open(ctx context.Context, access Access) (ChatSummary, error)
	List(ctx context.Context, access Access) ([]ChatSummary, error)
	Say(ctx context.Context, access Access, chatID, body string, picks Picks) error
	Retry(ctx context.Context, access Access, chatID, lineID string, picks *Picks) error
	Stop(ctx context.Context, access Access, chatID string) error
	Delete(ctx context.Context, access Access, chatID string) error
	Read(ctx context.Context, access Access, chatID string) (Chat, error)
}

// Stop ends the running reply of a chat.
func Stop(ctx context.Context, book Book, access Access, chatID string) error {
	if err := caller(access); err != nil {
		return err
	}
	if _, err := uuid.Parse(chatID); err != nil {
		return ErrChat
	}
	return book.Stop(ctx, access, chatID)
}

// Delete removes a chat owned by access.UserID.
func Delete(ctx context.Context, book Book, access Access, chatID string) error {
	if err := caller(access); err != nil {
		return err
	}
	if _, err := uuid.Parse(chatID); err != nil {
		return ErrChat
	}
	if !slices.Contains(strings.Split(access.Allow, ","), "chat:delete") {
		return ErrAccess
	}
	return book.Delete(ctx, access, chatID)
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

// Say appends one line. picks say how it is answered: the route, the pinned
// model and the agent. An empty route takes the install's chat route.
func Say(ctx context.Context, book Book, access Access, chatID, body string, picks Picks) error {
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
	picks, err = cleanPicks(picks)
	if err != nil {
		return err
	}
	return book.Say(ctx, access, chatID, body, picks)
}

// Retry asks again for the reply to the user line that a failed assistant
// line left unanswered. picks are the ones made now; with none given the
// picks stored on the user line are used again, and an empty route then
// takes the install's chat route.
func Retry(ctx context.Context, book Book, access Access, chatID, lineID string, picks *Picks) error {
	if err := caller(access); err != nil {
		return err
	}
	if _, err := uuid.Parse(chatID); err != nil {
		return ErrChat
	}
	if _, err := uuid.Parse(lineID); err != nil {
		return ErrNoLine
	}
	if picks == nil {
		return book.Retry(ctx, access, chatID, lineID, nil)
	}
	cleaned, err := cleanPicks(*picks)
	if err != nil {
		return err
	}
	return book.Retry(ctx, access, chatID, lineID, &cleaned)
}

// cleanPicks checks each pick of a line.
func cleanPicks(picks Picks) (Picks, error) {
	route, err := domain.Route(picks.Route)
	if err != nil {
		return Picks{}, err
	}
	account, model, err := domain.Pin(picks.PinAccount, picks.PinModel)
	if err != nil {
		return Picks{}, err
	}
	agent, err := domain.Agent(picks.AgentID)
	if err != nil {
		return Picks{}, err
	}
	return Picks{Route: route, PinAccount: account, PinModel: model, AgentID: agent}, nil
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
