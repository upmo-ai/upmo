package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/chat/domain"
)

const (
	agentA = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaa1"
	agentB = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaa2"
)

// agentBook keeps agents in memory and records what the use case asked it to
// write.
type agentBook struct {
	rows    map[string]Agent
	created []AgentFields
	global  bool
	written []AgentFields
	deleted []string
	err     error
}

func (b *agentBook) ListAgents(context.Context, Access) ([]Agent, error) { return nil, b.err }

func (b *agentBook) GetAgent(_ context.Context, _ Access, id string) (Agent, error) {
	if row, ok := b.rows[id]; ok {
		return row, nil
	}
	return Agent{}, ErrAgentNotFound
}

func (b *agentBook) CreateAgent(_ context.Context, _ Access, in AgentFields, global bool) (Agent, error) {
	b.created = append(b.created, in)
	b.global = global
	return Agent{ID: agentA, Name: in.Name}, b.err
}

func (b *agentBook) UpdateAgent(_ context.Context, _ Access, id string, in AgentFields) (Agent, error) {
	b.written = append(b.written, in)
	return Agent{ID: id, Name: in.Name}, b.err
}

func (b *agentBook) DeleteAgent(_ context.Context, _ Access, id string) error {
	b.deleted = append(b.deleted, id)
	return b.err
}

var (
	memberAccess = Access{UserID: userID, Allow: "chat:create,chat:view,chat:edit"}
	adminAccess  = Access{UserID: userID, Allow: "chat:create,chat:view,chat:edit,settings:manage"}
)

func TestAMemberCannotCreateAGlobalAgentAndTheBookIsNotAsked(t *testing.T) {
	book := &agentBook{}
	_, err := CreateAgent(context.Background(), book, memberAccess, AgentFields{Name: "House", Enabled: true}, true)
	if !errors.Is(err, ErrAgentGlobal) || len(book.created) != 0 {
		t.Fatalf("err = %v created = %+v", err, book.created)
	}
	if _, err := CreateAgent(context.Background(), book, adminAccess, AgentFields{Name: "House", Enabled: true}, true); err != nil || !book.global {
		t.Fatalf("admin: err = %v global = %v", err, book.global)
	}
	if _, err := CreateAgent(context.Background(), book, memberAccess, AgentFields{Name: "Mine", Enabled: true}, false); err != nil || book.global {
		t.Fatalf("member's own: err = %v global = %v", err, book.global)
	}
}

func TestCreateAgentCleansEveryFieldBeforeTheBookSeesIt(t *testing.T) {
	book := &agentBook{}
	_, err := CreateAgent(context.Background(), book, memberAccess, AgentFields{
		Name: "  Research ", Description: " d ", PromptOverlay: " be brief ", Route: " fast ",
		ModelProvider: " OpenAI ", ModelID: " gpt-x ", Enabled: true,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := AgentFields{Name: "Research", Description: "d", PromptOverlay: "be brief", Route: "fast", ModelProvider: "OpenAI", ModelID: "gpt-x", Enabled: true}
	if len(book.created) != 1 || book.created[0] != want {
		t.Fatalf("created = %+v", book.created)
	}
	for name, in := range map[string]AgentFields{
		"no name":      {},
		"half a model": {Name: "a", ModelProvider: "OpenAI"},
		"bad route":    {Name: "a", Route: "has space"},
		"long overlay": {Name: "a", PromptOverlay: strings.Repeat("o", domain.AgentOverlayRunes+1)},
	} {
		book.created = nil
		var invalid domain.Invalid
		if _, err := CreateAgent(context.Background(), book, memberAccess, in, false); !errors.As(err, &invalid) || len(book.created) != 0 {
			t.Fatalf("%s: err = %v created = %+v", name, err, book.created)
		}
	}
}

func TestChangeAgentMergesTheChangeIntoTheStoredAgent(t *testing.T) {
	book := &agentBook{rows: map[string]Agent{agentA: {
		ID: agentA, Name: "Research", Description: "d", PromptOverlay: "p", Route: "deep",
		ModelProvider: "OpenAI", ModelID: "gpt-x", Enabled: true, Editable: true,
	}}}
	name, none, off := "Renamed", "", false
	_, err := ChangeAgent(context.Background(), book, memberAccess, agentA, AgentChange{
		Name: &name, Route: &none, Enabled: &off,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := AgentFields{Name: "Renamed", Description: "d", PromptOverlay: "p", Route: "", ModelProvider: "OpenAI", ModelID: "gpt-x", Enabled: false}
	if len(book.written) != 1 || book.written[0] != want {
		t.Fatalf("written = %+v, want %+v", book.written, want)
	}

	// Clearing only the provider leaves half a model, which is refused.
	book.written = nil
	var invalid domain.Invalid
	if _, err := ChangeAgent(context.Background(), book, memberAccess, agentA, AgentChange{ModelProvider: &none}); !errors.As(err, &invalid) || len(book.written) != 0 {
		t.Fatalf("half a model: err = %v written = %+v", err, book.written)
	}
}

func TestChangeAndDeleteRefuseWhatThePersonMayNotChange(t *testing.T) {
	book := &agentBook{rows: map[string]Agent{
		agentA: {ID: agentA, Name: "House", Global: true, Editable: false},
	}}
	name := "x"
	if _, err := ChangeAgent(context.Background(), book, memberAccess, agentA, AgentChange{Name: &name}); !errors.Is(err, ErrAgentGlobal) || len(book.written) != 0 {
		t.Fatalf("change a global agent: err = %v written = %+v", err, book.written)
	}
	if err := DeleteAgent(context.Background(), book, memberAccess, agentA); !errors.Is(err, ErrAgentGlobal) || len(book.deleted) != 0 {
		t.Fatalf("delete a global agent: err = %v deleted = %v", err, book.deleted)
	}
	if _, err := ChangeAgent(context.Background(), book, memberAccess, agentB, AgentChange{Name: &name}); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("change an agent they cannot see: %v", err)
	}
	if err := DeleteAgent(context.Background(), book, memberAccess, agentB); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("delete an agent they cannot see: %v", err)
	}
	if _, err := ChangeAgent(context.Background(), book, memberAccess, "research", AgentChange{}); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("change a malformed id: %v", err)
	}
	book.rows[agentA] = Agent{ID: agentA, Name: "House", Global: true, Editable: true}
	if err := DeleteAgent(context.Background(), book, adminAccess, agentA); err != nil || len(book.deleted) != 1 {
		t.Fatalf("admin delete: err = %v deleted = %v", err, book.deleted)
	}
}

func TestAgentCallsNeedASignedInPerson(t *testing.T) {
	book := &agentBook{}
	if _, err := ListAgents(context.Background(), book, Access{}); !errors.Is(err, ErrAccess) {
		t.Fatalf("list: %v", err)
	}
	if _, err := CreateAgent(context.Background(), book, Access{}, AgentFields{Name: "a"}, false); !errors.Is(err, ErrAccess) {
		t.Fatalf("create: %v", err)
	}
	got, err := ListAgents(context.Background(), book, memberAccess)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("an empty list is an empty list, got %#v %v", got, err)
	}
}

func TestAgentCallsNeedNoChatPermission(t *testing.T) {
	book := &agentBook{}
	signedIn := Access{UserID: userID}
	if _, err := ListAgents(context.Background(), book, signedIn); err != nil {
		t.Fatalf("list with no permission: %v", err)
	}
	if _, err := CreateAgent(context.Background(), book, signedIn, AgentFields{Name: "Mine", Enabled: true}, false); err != nil {
		t.Fatalf("create own with no permission: %v", err)
	}
	if _, err := CreateAgent(context.Background(), book, signedIn, AgentFields{Name: "H", Enabled: true}, true); !errors.Is(err, ErrAgentGlobal) {
		t.Fatalf("global with no permission: %v", err)
	}
	managerOnly := Access{UserID: userID, Allow: "settings:manage"}
	if _, err := CreateAgent(context.Background(), book, managerOnly, AgentFields{Name: "H", Enabled: true}, true); err != nil {
		t.Fatalf("global for a settings-only manager: %v", err)
	}
	if _, err := ListAgents(context.Background(), book, Access{UserID: "not-a-uuid", Allow: "chat:view"}); !errors.Is(err, ErrAccess) {
		t.Fatalf("a bad person id: %v", err)
	}
}
