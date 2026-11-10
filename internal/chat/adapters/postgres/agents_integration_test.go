//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/chat/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

var (
	// adminAccess may write the global agents; ownerAccess and otherAccess,
	// two members, may not.
	adminAccess  = app.Access{UserID: "018f3b2c-7a10-7c4a-8e11-999999999999", Allow: "chat:create,chat:view,chat:edit,settings:manage"}
	memberAccess = app.Access{UserID: ownerUser, Allow: "chat:create,chat:view,chat:edit"}
	otherMember  = app.Access{UserID: otherUser, Allow: "chat:create,chat:view,chat:edit"}
)

func agentStore(t *testing.T) postgres.Store {
	t.Helper()
	return postgres.Store{Pool: pgtest.App(t, up(t)), ReplyRoute: "chat"}
}

func fields(name string) app.AgentFields {
	return app.AgentFields{Name: name, PromptOverlay: "Be brief.", Enabled: true}
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
	mine, err := store.CreateAgent(ctx, memberAccess, fields("Mine"), false)
	if err != nil {
		t.Fatal(err)
	}
	if !mine.Editable || mine.Global {
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
	// An admin sees no one's own agents either: settings:manage reaches the
	// global ones only.
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

func TestAMemberCannotWriteAGlobalAgentButEveryoneReadsIt(t *testing.T) {
	store := agentStore(t)
	ctx := context.Background()
	if _, err := store.CreateAgent(ctx, memberAccess, fields("House"), true); !errors.Is(err, app.ErrAgentGlobal) {
		t.Fatalf("member creates a global agent: %v, want refused", err)
	}
	house, err := store.CreateAgent(ctx, adminAccess, fields("House"), true)
	if err != nil {
		t.Fatal(err)
	}
	if !house.Global || !house.Editable {
		t.Fatalf("global agent for the admin = %+v", house)
	}

	for _, who := range []app.Access{memberAccess, otherMember} {
		got, err := store.ListAgents(ctx, who)
		if err != nil || len(got) != 1 || got[0].ID != house.ID || !got[0].Global || got[0].Editable {
			t.Fatalf("%s lists %+v %v, want the global agent, read only", who.UserID, got, err)
		}
		if _, err := store.Agent(ctx, who, house.ID); err != nil {
			t.Fatalf("%s cannot use the global agent: %v", who.UserID, err)
		}
		if _, err := store.UpdateAgent(ctx, who, house.ID, fields("Mine now")); !errors.Is(err, app.ErrAgentGlobal) {
			t.Fatalf("%s updates a global agent: %v, want refused", who.UserID, err)
		}
		if err := store.DeleteAgent(ctx, who, house.ID); !errors.Is(err, app.ErrAgentGlobal) {
			t.Fatalf("%s deletes a global agent: %v, want refused", who.UserID, err)
		}
	}
	if got, err := store.GetAgent(ctx, adminAccess, house.ID); err != nil || got.Name != "House" {
		t.Fatalf("the global agent after the refusals = %+v %v", got, err)
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

func TestADisabledGlobalAgentIsHiddenFromMembersAndUnusableInAReply(t *testing.T) {
	store := agentStore(t)
	ctx := context.Background()
	off := fields("Off")
	off.Enabled = false
	house, err := store.CreateAgent(ctx, adminAccess, off, true)
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
	own, err := store.CreateAgent(ctx, memberAccess, mine, false)
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
	if _, err := store.CreateAgent(ctx, memberAccess, fields("Research"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAgent(ctx, memberAccess, fields("RESEARCH"), false); !errors.Is(err, app.ErrAgentDuplicate) {
		t.Fatalf("same name, other case: %v, want duplicate", err)
	}
	// Another person may use the name, and so may the global scope.
	if _, err := store.CreateAgent(ctx, otherMember, fields("Research"), false); err != nil {
		t.Fatalf("the other person: %v", err)
	}
	if _, err := store.CreateAgent(ctx, adminAccess, fields("research"), true); err != nil {
		t.Fatalf("a global agent with the name: %v", err)
	}
	if _, err := store.CreateAgent(ctx, adminAccess, fields("RESEARCH"), true); !errors.Is(err, app.ErrAgentDuplicate) {
		t.Fatalf("two global agents: %v, want duplicate", err)
	}
	// A rename into a taken name is refused as well.
	second, err := store.CreateAgent(ctx, memberAccess, fields("Writing"), false)
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
	made, err := store.CreateAgent(ctx, memberAccess, in, false)
	if err != nil {
		t.Fatal(err)
	}
	if made.Name != "Full" || made.Description != "d" || made.PromptOverlay != "p" || made.Route != "fast" ||
		made.ModelProvider != "OpenAI" || made.ModelID != "gpt-x" || !made.Enabled {
		t.Fatalf("created = %+v", made)
	}
	plain, err := store.CreateAgent(ctx, memberAccess, fields("Plain"), false)
	if err != nil || plain.Route != "" || plain.ModelProvider != "" || plain.ModelID != "" {
		t.Fatalf("plain = %+v %v", plain, err)
	}
	half := fields("Half")
	half.ModelProvider = "OpenAI"
	if _, err := store.CreateAgent(ctx, memberAccess, half, false); err == nil {
		t.Fatal("the database stored a provider without a model")
	}
	cleared, err := store.UpdateAgent(ctx, memberAccess, made.ID, fields("Full"))
	if err != nil || cleared.Route != "" || cleared.ModelProvider != "" || cleared.ModelID != "" {
		t.Fatalf("cleared = %+v %v", cleared, err)
	}
}

func TestAgentsAreForcedUnderRowSecurityForTheTableOwnerToo(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	if _, err := store.CreateAgent(ctx, memberAccess, fields("Mine"), false); err != nil {
		t.Fatal(err)
	}
	var enabled, forced bool
	if err := owner.QueryRow(ctx, `
		SELECT relrowsecurity, relforcerowsecurity FROM pg_class
		WHERE oid = 'chat.agents'::regclass`).Scan(&enabled, &forced); err != nil || !enabled || !forced {
		t.Fatalf("row security enabled=%v forced=%v %v", enabled, forced, err)
	}
	// Even a session with no person at all sees nothing: not an own agent, and
	// not an enabled or disabled global one either, whatever it allows.
	if _, err := store.CreateAgent(ctx, adminAccess, fields("House"), true); err != nil {
		t.Fatal(err)
	}
	off := fields("House off")
	off.Enabled = false
	if _, err := store.CreateAgent(ctx, adminAccess, off, true); err != nil {
		t.Fatal(err)
	}
	for _, allow := range []string{"", "settings:manage"} {
		tx, err := store.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('upmo.allow', $1, true)`, allow); err != nil {
			t.Fatal(err)
		}
		var n int
		err = tx.QueryRow(ctx, `SELECT count(*) FROM chat.agents`).Scan(&n)
		_ = tx.Rollback(ctx)
		if err != nil || n != 0 {
			t.Fatalf("a session with no person (allow %q) sees %d agents (%v)", allow, n, err)
		}
	}
	var n int
	if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM chat.agents`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a clean session sees %d agents (%v)", n, err)
	}
}

func TestAgentsNeedNoChatPermission(t *testing.T) {
	store := agentStore(t)
	ctx := context.Background()
	// A settings-only manager has no chat permission at all.
	manager := app.Access{UserID: "018f3b2c-7a10-7c4a-8e11-888888888888", Allow: "settings:manage"}
	// A person with no permission of any kind is still signed in.
	bare := app.Access{UserID: "018f3b2c-7a10-7c4a-8e11-777777777777"}

	house, err := app.CreateAgent(ctx, store, manager, fields("House"), true)
	if err != nil {
		t.Fatalf("a settings-only manager creates a global agent: %v", err)
	}
	if _, err := app.CreateAgent(ctx, store, bare, fields("Nope"), true); !errors.Is(err, app.ErrAgentGlobal) {
		t.Fatalf("a person with no permission creates a global agent: %v", err)
	}
	if _, err := app.CreateAgent(ctx, store, memberAccess, fields("Nope"), true); !errors.Is(err, app.ErrAgentGlobal) {
		t.Fatalf("an ordinary member creates a global agent: %v", err)
	}
	mine, err := app.CreateAgent(ctx, store, bare, fields("Mine"), false)
	if err != nil || !mine.Editable {
		t.Fatalf("a person with no permission creates their own: %+v %v", mine, err)
	}
	got, err := app.ListAgents(ctx, store, bare)
	if err != nil || len(got) != 2 || got[0].ID != mine.ID || got[1].ID != house.ID || got[1].Editable {
		t.Fatalf("the person lists %v %v, want own then the global one read only", names(got), err)
	}
	if err := app.DeleteAgent(ctx, store, bare, house.ID); !errors.Is(err, app.ErrAgentGlobal) {
		t.Fatalf("delete a global agent without settings:manage: %v", err)
	}
	if _, err := app.ListAgents(ctx, store, app.Access{}); !errors.Is(err, app.ErrAccess) {
		t.Fatalf("no person: %v", err)
	}
}
