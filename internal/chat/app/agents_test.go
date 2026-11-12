package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/chat/domain"
)

const (
	agentA = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaa1"
	agentB = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaa2"
)

type agentBook struct {
	rows    map[string]Agent
	created []AgentFields
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

func (b *agentBook) CreateAgent(_ context.Context, _ Access, in AgentFields) (Agent, error) {
	b.created = append(b.created, in)
	return Agent{ID: agentA, Name: in.Name, Share: AgentShare{Visibility: in.Visibility, CanManage: true}}, b.err
}

func (b *agentBook) UpdateAgent(_ context.Context, _ Access, id string, in AgentFields) (Agent, error) {
	b.written = append(b.written, in)
	return Agent{ID: id, Name: in.Name, Share: AgentShare{CanManage: true}}, b.err
}

func (b *agentBook) DeleteAgent(_ context.Context, _ Access, id string) error {
	b.deleted = append(b.deleted, id)
	return b.err
}

var (
	memberAccess = Access{UserID: userID, Allow: "agent:create,agent:view,agent:edit,agent:delete,agent:share"}
	adminAccess  = Access{UserID: userID, Allow: "agent:create,agent:view,agent:edit,agent:delete,agent:share,settings:manage"}
	viewerAccess = Access{UserID: userID, Allow: "agent:view"}
)

func TestAMemberCannotCreateAWorkspaceAgentAndTheBookIsNotAsked(t *testing.T) {
	book := &agentBook{}
	_, err := CreateAgent(context.Background(), book, nil, memberAccess, AgentFields{Name: "House", Enabled: true, Visibility: domain.VisibilityEveryone})
	if !errors.Is(err, ErrAgentManaged) || len(book.created) != 0 {
		t.Fatalf("err = %v created = %+v", err, book.created)
	}
	if _, err := CreateAgent(context.Background(), book, nil, adminAccess, AgentFields{Name: "House", Enabled: true, Visibility: domain.VisibilityEveryone}); err != nil {
		t.Fatalf("admin: err = %v", err)
	}
	if _, err := CreateAgent(context.Background(), book, nil, memberAccess, AgentFields{Name: "Mine", Enabled: true}); err != nil {
		t.Fatalf("member's own: err = %v", err)
	}
}

func TestCreateAgentCleansEveryFieldBeforeTheBookSeesIt(t *testing.T) {
	book := &agentBook{}
	_, err := CreateAgent(context.Background(), book, nil, memberAccess, AgentFields{
		Name: "  Research ", Description: " d ", PromptOverlay: " be brief ", Route: " fast ",
		ModelProvider: " OpenAI ", ModelID: " gpt-x ", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := AgentFields{Name: "Research", Description: "d", PromptOverlay: "be brief", Route: "fast",
		ModelProvider: "OpenAI", ModelID: "gpt-x", Enabled: true, Visibility: domain.VisibilityPrivate}
	if !reflect.DeepEqual(book.created[0], want) {
		t.Fatalf("created = %+v, want %+v", book.created[0], want)
	}
}

func TestCreateAgentRefusesBadFields(t *testing.T) {
	book := &agentBook{}
	for _, in := range []AgentFields{{Name: ""}, {Name: strings.Repeat("a", 61)}} {
		var invalid domain.Invalid
		if _, err := CreateAgent(context.Background(), book, nil, memberAccess, in); !errors.As(err, &invalid) || len(book.created) != 0 {
			t.Fatalf("in = %+v err = %v", in, err)
		}
	}
}

func TestChangeAndDeleteRefuseAnAgentThePersonCannotManage(t *testing.T) {
	book := &agentBook{rows: map[string]Agent{
		agentA: {ID: agentA, Name: "House", Share: AgentShare{Visibility: domain.VisibilityEveryone, CanManage: false}},
	}}
	name := "X"
	if _, err := ChangeAgent(context.Background(), book, nil, memberAccess, agentA, AgentChange{Name: &name}); !errors.Is(err, ErrAgentManaged) || len(book.written) != 0 {
		t.Fatalf("change: err = %v written = %+v", err, book.written)
	}
	if err := DeleteAgent(context.Background(), book, memberAccess, agentA); !errors.Is(err, ErrAgentManaged) || len(book.deleted) != 0 {
		t.Fatalf("delete: err = %v deleted = %v", err, book.deleted)
	}
}

func TestAViewerCannotCreate(t *testing.T) {
	book := &agentBook{}
	if _, err := CreateAgent(context.Background(), book, nil, viewerAccess, AgentFields{Name: "Nope", Enabled: true}); !errors.Is(err, ErrAgentCreate) {
		t.Fatalf("viewer: %v", err)
	}
}

func TestCreateAndListRefuseNoPerson(t *testing.T) {
	book := &agentBook{}
	if _, err := CreateAgent(context.Background(), book, nil, Access{}, AgentFields{Name: "a"}); !errors.Is(err, ErrAccess) {
		t.Fatalf("create: %v", err)
	}
	if _, err := ListAgents(context.Background(), book, Access{}); !errors.Is(err, ErrAccess) {
		t.Fatalf("list: %v", err)
	}
}

func TestChangeAgentClearsTeamsWhenVisibilityLeavesTeam(t *testing.T) {
	book := &agentBook{rows: map[string]Agent{
		agentA: {
			ID: agentA, Name: "Finance", Share: AgentShare{
				Visibility: domain.VisibilityTeam, TeamIDs: []string{"10000000-0000-7000-8000-000000000007"}, CanManage: true,
			},
		},
	}}
	everyone := domain.VisibilityEveryone
	if _, err := ChangeAgent(context.Background(), book, nil, adminAccess, agentA, AgentChange{Visibility: &everyone}); err != nil {
		t.Fatal(err)
	}
	if len(book.written) != 1 || book.written[0].Visibility != domain.VisibilityEveryone || len(book.written[0].TeamIDs) != 0 {
		t.Fatalf("everyone: written = %+v", book.written[0])
	}
	private := domain.VisibilityPrivate
	if _, err := ChangeAgent(context.Background(), book, nil, adminAccess, agentA, AgentChange{Visibility: &private}); err != nil {
		t.Fatal(err)
	}
	if book.written[1].Visibility != domain.VisibilityPrivate || len(book.written[1].TeamIDs) != 0 {
		t.Fatalf("private: written = %+v", book.written[1])
	}
}
