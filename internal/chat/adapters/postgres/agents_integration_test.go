//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/chat/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

var (
	memberAgentAllow = "agent:create,agent:view,agent:edit,agent:delete,agent:share"
	adminAgentAllow  = memberAgentAllow + ",settings:manage"
	adminAccess      = app.Access{UserID: "018f3b2c-7a10-7c4a-8e11-999999999999", Allow: adminAgentAllow}
	memberAccess     = app.Access{UserID: ownerUser, Allow: memberAgentAllow}
	otherMember      = app.Access{UserID: otherUser, Allow: memberAgentAllow}
)

func agentStore(t *testing.T) postgres.Store {
	t.Helper()
	return postgres.Store{Pool: pgtest.App(t, up(t)), ReplyRoute: "chat"}
}

func fields(name string) app.AgentFields {
	return app.AgentFields{Name: name, PromptOverlay: "Be brief.", Enabled: true}
}

func sharedFields(name string) app.AgentFields {
	f := fields(name)
	f.Visibility = domain.VisibilityEveryone
	return f
}

func names(agents []app.Agent) []string {
	out := make([]string, 0, len(agents))
	for _, a := range agents {
		out = append(out, a.Name)
	}
	return out
}

func TestAnotherPersonCannotSeeOrChangeMyAgent(t *testing.T) {
	store := agentStore(t)
	ctx := context.Background()
	mine, err := store.CreateAgent(ctx, memberAccess, fields("Mine"))
	if err != nil {
		t.Fatal(err)
	}
	if !mine.Share.CanManage || mine.Share.Visibility != domain.VisibilityPrivate {
		t.Fatalf("my agent = %+v", mine)
	}

	if got, err := store.ListAgents(ctx, otherMember); err != nil || len(got) != 0 {
		t.Fatalf("the other person lists %v %v, want nothing", names(got), err)
	}
	if _, err := store.GetAgent(ctx, otherMember, mine.ID); !errors.Is(err, app.ErrAgentNotFound) {
		t.Fatalf("get = %v, want not found", err)
	}
	if _, err := store.Agent(ctx, otherMember, mine.ID); !errors.Is(err, app.ErrAgentNotFound) {
		t.Fatalf("reply lookup = %v, want not found", err)
	}
	if _, err := store.UpdateAgent(ctx, otherMember, mine.ID, fields("Taken")); !errors.Is(err, app.ErrAgentNotFound) {
		t.Fatalf("update = %v, want not found", err)
	}
	if err := store.DeleteAgent(ctx, otherMember, mine.ID); !errors.Is(err, app.ErrAgentNotFound) {
		t.Fatalf("delete = %v, want not found", err)
	}
	if got, err := store.ListAgents(ctx, adminAccess); err != nil || len(got) != 0 {
		t.Fatalf("the admin lists %v %v, want nothing", names(got), err)
	}
	if err := store.DeleteAgent(ctx, adminAccess, mine.ID); !errors.Is(err, app.ErrAgentNotFound) {
		t.Fatalf("admin delete of a member's agent = %v, want not found", err)
	}

	got, err := store.GetAgent(ctx, memberAccess, mine.ID)
	if err != nil || got.Name != "Mine" {
		t.Fatalf("I read %+v %v, the other person's tries changed nothing", got, err)
	}
}

func TestAMemberCannotShareAWorkspaceAgentButEveryoneReadsIt(t *testing.T) {
	store := agentStore(t)
	ctx := context.Background()
	if _, err := store.CreateAgent(ctx, memberAccess, sharedFields("House")); !errors.Is(err, app.ErrAgentManaged) {
		t.Fatalf("member creates a workspace agent: %v, want refused", err)
	}
	house, err := store.CreateAgent(ctx, adminAccess, sharedFields("House"))
	if err != nil {
		t.Fatal(err)
	}
	if house.Share.Visibility != domain.VisibilityEveryone || !house.Share.CanManage {
		t.Fatalf("workspace agent for the admin = %+v", house)
	}

	for _, who := range []app.Access{memberAccess, otherMember} {
		got, err := store.ListAgents(ctx, who)
		if err != nil || len(got) != 1 || got[0].ID != house.ID || !got[0].Share.CanUse || got[0].Share.CanManage {
			t.Fatalf("%s lists %+v %v, want the workspace agent, read only", who.UserID, got, err)
		}
		if _, err := store.Agent(ctx, who, house.ID); err != nil {
			t.Fatalf("%s cannot use the workspace agent: %v", who.UserID, err)
		}
		if _, err := store.UpdateAgent(ctx, who, house.ID, fields("Mine now")); !errors.Is(err, app.ErrAgentManaged) {
			t.Fatalf("%s updates a workspace agent: %v, want refused", who.UserID, err)
		}
		if err := store.DeleteAgent(ctx, who, house.ID); !errors.Is(err, app.ErrAgentManaged) {
			t.Fatalf("%s deletes a workspace agent: %v, want refused", who.UserID, err)
		}
	}
	if got, err := store.GetAgent(ctx, adminAccess, house.ID); err != nil || got.Name != "House" {
		t.Fatalf("the workspace agent after the refusals = %+v %v", got, err)
	}

	changed, err := store.UpdateAgent(ctx, adminAccess, house.ID, fields("House 2"))
	if err != nil || changed.Name != "House 2" {
		t.Fatalf("admin update = %+v %v", changed, err)
	}
	if err := store.DeleteAgent(ctx, adminAccess, house.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.ListAgents(ctx, memberAccess); len(got) != 0 {
		t.Fatalf("after the delete %v remain", names(got))
	}
}

func TestADisabledWorkspaceAgentIsHiddenFromMembersAndUnusableInAReply(t *testing.T) {
	store := agentStore(t)
	ctx := context.Background()
	off := sharedFields("Off")
	off.Enabled = false
	house, err := store.CreateAgent(ctx, adminAccess, off)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := store.ListAgents(ctx, memberAccess); len(got) != 0 {
		t.Fatalf("a member lists %v", names(got))
	}
	if got, _ := store.ListAgents(ctx, adminAccess); len(got) != 1 {
		t.Fatalf("the admin lists %v, want the disabled agent to manage", names(got))
	}
	if _, err := store.Agent(ctx, adminAccess, house.ID); !errors.Is(err, app.ErrAgentNotFound) {
		t.Fatalf("a reply with a disabled agent: %v, want not available", err)
	}

	mine := fields("Mine off")
	mine.Enabled = false
	own, err := store.CreateAgent(ctx, memberAccess, mine)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Agent(ctx, memberAccess, own.ID); !errors.Is(err, app.ErrAgentNotFound) {
		t.Fatalf("a reply with my disabled agent: %v, want not available", err)
	}
	if _, err := store.GetAgent(ctx, memberAccess, own.ID); err != nil {
		t.Fatalf("I can still manage my disabled agent: %v", err)
	}
}

func TestAgentNamesAreUniquePerOwnerWithoutRegardToCase(t *testing.T) {
	store := agentStore(t)
	ctx := context.Background()
	if _, err := store.CreateAgent(ctx, memberAccess, fields("Research")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAgent(ctx, memberAccess, fields("RESEARCH")); !errors.Is(err, app.ErrAgentDuplicate) {
		t.Fatalf("same name, other case: %v, want duplicate", err)
	}
	if _, err := store.CreateAgent(ctx, otherMember, fields("Research")); err != nil {
		t.Fatalf("the other person: %v", err)
	}
	if _, err := store.CreateAgent(ctx, adminAccess, sharedFields("research")); err != nil {
		t.Fatalf("a workspace agent with the name: %v", err)
	}
	if _, err := store.CreateAgent(ctx, adminAccess, sharedFields("RESEARCH")); !errors.Is(err, app.ErrAgentDuplicate) {
		t.Fatalf("two workspace agents: %v, want duplicate", err)
	}
	second, err := store.CreateAgent(ctx, memberAccess, fields("Writing"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateAgent(ctx, memberAccess, second.ID, fields("research")); !errors.Is(err, app.ErrAgentDuplicate) {
		t.Fatalf("rename: %v, want duplicate", err)
	}
}

func TestAgentFieldsRoundTripAndTheDatabaseRefusesHalfAModel(t *testing.T) {
	store := agentStore(t)
	ctx := context.Background()
	in := app.AgentFields{
		Name: "Full", Description: "d", PromptOverlay: "p", Route: "fast",
		ModelProvider: "OpenAI", ModelID: "gpt-x", Enabled: true,
	}
	made, err := store.CreateAgent(ctx, memberAccess, in)
	if err != nil {
		t.Fatal(err)
	}
	if made.Name != "Full" || made.Description != "d" || made.PromptOverlay != "p" || made.Route != "fast" ||
		made.ModelProvider != "OpenAI" || made.ModelID != "gpt-x" || !made.Enabled {
		t.Fatalf("created = %+v", made)
	}
	plain, err := store.CreateAgent(ctx, memberAccess, fields("Plain"))
	if err != nil || plain.Route != "" || plain.ModelProvider != "" || plain.ModelID != "" {
		t.Fatalf("plain = %+v %v", plain, err)
	}
	half := fields("Half")
	half.ModelProvider = "OpenAI"
	if _, err := store.CreateAgent(ctx, memberAccess, half); err == nil {
		t.Fatal("the database stored a provider without a model")
	}
	cleared, err := store.UpdateAgent(ctx, memberAccess, made.ID, fields("Full"))
	if err != nil || cleared.Route != "" || cleared.ModelProvider != "" || cleared.ModelID != "" {
		t.Fatalf("cleared = %+v %v", cleared, err)
	}
}

func agentCountInSession(ctx context.Context, pool *postgres.Store, allow, where string) (int, error) {
	tx, err := pool.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('upmo.allow', $1, true)`, allow); err != nil {
		return 0, err
	}
	q := `SELECT count(*) FROM chat.agents`
	if where != "" {
		q += ` WHERE ` + where
	}
	var n int
	err = tx.QueryRow(ctx, q).Scan(&n)
	return n, err
}

func TestAgentsAreForcedUnderRowSecurityForTheTableOwnerToo(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	private, err := store.CreateAgent(ctx, memberAccess, fields("Mine"))
	if err != nil {
		t.Fatal(err)
	}
	if private.Share.Visibility != domain.VisibilityPrivate {
		t.Fatalf("private agent = %+v", private)
	}
	got, err := store.ListAgents(ctx, memberAccess)
	if err != nil || len(got) != 1 || got[0].ID != private.ID {
		t.Fatalf("the owner lists %v %v, want their private agent", names(got), err)
	}
	var enabled, forced bool
	if err := owner.QueryRow(ctx, `
		SELECT relrowsecurity, relforcerowsecurity FROM pg_class
		WHERE oid = 'chat.agents'::regclass`).Scan(&enabled, &forced); err != nil || !enabled || !forced {
		t.Fatalf("row security enabled=%v forced=%v %v", enabled, forced, err)
	}
	if _, err := store.CreateAgent(ctx, adminAccess, sharedFields("House")); err != nil {
		t.Fatal(err)
	}
	off := sharedFields("House off")
	off.Enabled = false
	if _, err := store.CreateAgent(ctx, adminAccess, off); err != nil {
		t.Fatal(err)
	}
	n, err := agentCountInSession(ctx, &store, "", "")
	if err != nil || n != 0 {
		t.Fatalf("a session with no person and no allow sees %d agents (%v)", n, err)
	}
	n, err = agentCountInSession(ctx, &store, adminAgentAllow, `visibility = 'private'`)
	if err != nil || n != 0 {
		t.Fatalf("allow without a person still sees %d private agents (%v)", n, err)
	}
	n, err = agentCountInSession(ctx, &store, adminAgentAllow, `visibility <> 'private'`)
	if err != nil || n != 2 {
		t.Fatalf("allow without a person sees %d shared agents (%v), want the workspace ones via may_manage", n, err)
	}
	n, err = agentCountInSession(ctx, &store, adminAgentAllow, "")
	if err != nil || n != 2 {
		t.Fatalf("a session with no person (allow %q) sees %d agents (%v), want shared only", adminAgentAllow, n, err)
	}
	if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM chat.agents`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a clean session sees %d agents (%v)", n, err)
	}
}

func TestAgentsNeedNoChatPermission(t *testing.T) {
	store := agentStore(t)
	ctx := context.Background()
	manager := app.Access{UserID: "018f3b2c-7a10-7c4a-8e11-888888888888", Allow: "settings:manage,agent:create,agent:view,agent:edit,agent:delete,agent:share"}
	bare := app.Access{UserID: "018f3b2c-7a10-7c4a-8e11-777777777777", Allow: "agent:create,agent:view,agent:edit,agent:delete,agent:share"}

	house, err := app.CreateAgent(ctx, store, nil, manager, sharedFields("House"))
	if err != nil {
		t.Fatalf("a settings manager creates a workspace agent: %v", err)
	}
	if _, err := app.CreateAgent(ctx, store, nil, bare, sharedFields("Nope")); !errors.Is(err, app.ErrAgentManaged) {
		t.Fatalf("a member without settings:manage shares: %v", err)
	}
	if _, err := app.CreateAgent(ctx, store, nil, memberAccess, sharedFields("Nope")); !errors.Is(err, app.ErrAgentManaged) {
		t.Fatalf("an ordinary member creates a workspace agent: %v", err)
	}
	mine, err := app.CreateAgent(ctx, store, nil, bare, fields("Mine"))
	if err != nil || !mine.Share.CanManage {
		t.Fatalf("a person creates their own: %+v %v", mine, err)
	}
	got, err := app.ListAgents(ctx, store, bare)
	if err != nil || len(got) != 2 || got[0].ID != mine.ID || got[1].ID != house.ID || got[1].Share.CanManage {
		t.Fatalf("the person lists %v %v, want own then the workspace one read only", names(got), err)
	}
	if err := app.DeleteAgent(ctx, store, bare, house.ID); !errors.Is(err, app.ErrAgentManaged) {
		t.Fatalf("delete a workspace agent without manage: %v", err)
	}
	if _, err := app.ListAgents(ctx, store, app.Access{}); !errors.Is(err, app.ErrAccess) {
		t.Fatalf("no person: %v", err)
	}
}
