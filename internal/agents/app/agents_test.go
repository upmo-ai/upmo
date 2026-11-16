package app

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/agents/domain"
)

type stubBook struct {
	gotCreate AgentFields
	gotUpdate AgentFields
	current   Agent
}

func (s *stubBook) ListAgents(context.Context, Access) ([]Agent, error) {
	return []Agent{s.current}, nil
}

func (s *stubBook) GetAgent(context.Context, Access, string) (Agent, error) {
	return s.current, nil
}

func (s *stubBook) AgentForUse(context.Context, Access, string) (Agent, []domain.Lesson, error) {
	return s.current, nil, nil
}

func (s *stubBook) CreateAgent(_ context.Context, _ Access, in AgentFields) (Agent, error) {
	s.gotCreate = in
	return Agent{ID: "018f3b2c-7a10-7c4a-8e11-111111111111", Name: in.Name, Slug: in.Slug, PlanApproval: in.PlanApproval, Version: 1}, nil
}

func (s *stubBook) UpdateAgent(_ context.Context, _ Access, id string, in AgentFields) (Agent, error) {
	s.gotUpdate = in
	return Agent{ID: id, Name: in.Name, Slug: in.Slug, PlanApproval: in.PlanApproval, Version: 2}, nil
}

func (s *stubBook) DeleteAgent(context.Context, Access, string) error { return nil }

func (s *stubBook) ListVersions(context.Context, Access, string) ([]Version, error) {
	return nil, nil
}

func (s *stubBook) GetVersion(context.Context, Access, string, int) (Version, error) {
	return Version{}, nil
}

func TestCreateAndChangeAgentValidation(t *testing.T) {
	const userID = "018f3b2c-7a10-7c4a-8e11-111111111111"
	const agentID = "018f3b2c-7a10-7c4a-8e11-222222222222"

	book := &stubBook{}
	member := Access{UserID: userID, Allow: "agent:create,agent:view,agent:edit,agent:delete"}
	if _, err := CreateAgent(context.Background(), book, nil, Access{UserID: userID, Allow: "agent:view"}, AgentFields{Name: "A"}); !errors.Is(err, ErrAgentCreate) {
		t.Fatalf("no agent:create = %v", err)
	}
	if _, err := CreateAgent(context.Background(), book, nil, member, AgentFields{Name: "A", Visibility: domain.VisibilityEveryone}); !errors.Is(err, ErrAgentManaged) {
		t.Fatalf("member shared = %v", err)
	}
	made, err := CreateAgent(context.Background(), book, nil, member, AgentFields{
		Name: "Purchase Invoices", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if made.Slug != "purchase-invoices" || made.PlanApproval != domain.PlanApprovalRisky {
		t.Fatalf("created = %+v", made)
	}

	book.current = Agent{
		ID:           agentID,
		Name:         "Purchase Invoices",
		Slug:         "purchase-invoices",
		PlanApproval: domain.PlanApprovalRisky,
		Share:        AgentShare{Visibility: domain.VisibilityPrivate, CanManage: true},
	}
	newName := "AP Clerk"
	if _, err := ChangeAgent(context.Background(), book, nil, member, agentID, AgentChange{Name: &newName}); err != nil {
		t.Fatal(err)
	}
	if book.gotUpdate.Name != "AP Clerk" || book.gotUpdate.Slug != "purchase-invoices" {
		t.Fatalf("update = %+v", book.gotUpdate)
	}
}
