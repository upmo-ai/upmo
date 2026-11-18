//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/agents/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/agents/app"
)

func TestOnlyTheOwnerTakesASharedAgentPrivate(t *testing.T) {
	w := newAgWorld(t)
	stmt := `UPDATE agents.agents SET visibility = 'private', team_ids = '{}' WHERE id = $1::uuid`
	for _, role := range []string{"owner", "admin", "member", "viewer", "custom"} {
		id := w.insertAgent(w.people[role], "shared "+role, "shared-"+role, "everyone")
		allow := w.allow(role)
		w.as(w.people[role], allow, "{}", func(tx pgx.Tx) {
			want := strings.Contains(allow, "settings:manage") && strings.Contains(allow, "agent:edit")
			n, vis, emptyTeams := w.updatedAndRead(tx, stmt, id)
			if (n == 1) != want {
				t.Errorf("%s took private: %d want %v", role, n, want)
			}
			if want && (vis != "private" || !emptyTeams) {
				t.Errorf("%s update read back vis %q emptyTeams %v", role, vis, emptyTeams)
			}
			if !want && vis != "everyone" {
				t.Errorf("%s was refused yet read back vis %q", role, vis)
			}
		})
	}

	memberShared := w.insertAgent(agMember, "member shared", "member-shared", "everyone")
	w.as(agAdmin, w.allow("admin"), "{}", func(tx pgx.Tx) {
		n, err := w.touched(tx, stmt, memberShared)
		if agAllowed(n, err) {
			t.Errorf("admin took a member's shared agent private: %d %v want denied", n, err)
		}
	})
}

// updatedAndRead runs stmt for one agent inside tx, reads the agent's visibility
// and team_ids back in the same transaction, then undoes the write. A refused
// update reports zero rows (or an error) and the unchanged row.
func (w *agWorld) updatedAndRead(tx pgx.Tx, stmt, id string) (n int64, vis string, emptyTeams bool) {
	w.t.Helper()
	ctx := context.Background()
	if _, err := tx.Exec(ctx, "SAVEPOINT ag_read"); err != nil {
		w.t.Fatal(err)
	}
	tag, err := tx.Exec(ctx, stmt, id)
	if err != nil {
		if _, rbErr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT ag_read"); rbErr != nil {
			w.t.Fatal(rbErr)
		}
	} else {
		n = tag.RowsAffected()
	}
	if err := tx.QueryRow(ctx, `SELECT visibility, team_ids = '{}'::uuid[] FROM agents.agents WHERE id = $1::uuid`, id).Scan(&vis, &emptyTeams); err != nil {
		w.t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT ag_read"); err != nil {
		w.t.Fatal(err)
	}
	return n, vis, emptyTeams
}

func TestChangingTeamAgentToEveryoneOrPrivateClearsTeamIDs(t *testing.T) {
	w := newAgWorld(t)
	ctx := context.Background()
	store := postgres.Store{Pool: w.app}
	admin := app.Access{UserID: agAdmin, Allow: w.allow("admin"), Teams: "{}"}
	readRow := func(id string) (vis string, emptyTeams bool) {
		if err := w.owner.QueryRow(ctx, `
			SELECT visibility, team_ids = '{}'::uuid[]
			FROM agents.agents WHERE id = $1::uuid`, id).Scan(&vis, &emptyTeams); err != nil {
			t.Fatal(err)
		}
		return vis, emptyTeams
	}
	everyone := agents.VisibilityEveryone
	if _, err := agents.ChangeAgent(ctx, store, nil, admin, w.shared["team"], agents.AgentChange{Visibility: &everyone}); err != nil {
		t.Fatalf("team to everyone: %v", err)
	}
	if vis, empty := readRow(w.shared["team"]); vis != "everyone" || !empty {
		t.Fatalf("after everyone: vis %q emptyTeams %v", vis, empty)
	}
	teamPrivate := w.insertAgent(agAdmin, "team private", "team-private", "team", agTeam)
	private := agents.VisibilityPrivate
	if _, err := agents.ChangeAgent(ctx, store, nil, admin, teamPrivate, agents.AgentChange{Visibility: &private}); err != nil {
		t.Fatalf("team to private: %v", err)
	}
	if vis, empty := readRow(teamPrivate); vis != "private" || !empty {
		t.Fatalf("after private: vis %q emptyTeams %v", vis, empty)
	}
}

func TestLessonsStayHiddenAndUntouchedWithoutAgentAccess(t *testing.T) {
	w := newAgWorld(t)
	ctx := context.Background()
	foreign := w.own["owner"]

	var n int
	if err := w.app.QueryRow(ctx, `SELECT count(*) FROM agents.lessons`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("no settings saw %d lessons", n)
	}

	w.as(agStranger, w.allow("member"), "{}", func(tx pgx.Tx) {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM agents.lessons WHERE agent_id = $1::uuid`, foreign).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("another person saw %d lessons of a private agent", n)
		}
		inserted, err := w.touched(tx, `
			INSERT INTO agents.lessons (agent_id, title, cue, body) VALUES ($1::uuid, 'Planted', 'Cue', 'Body')`, foreign)
		if agAllowed(inserted, err) {
			t.Error("another person inserted a lesson")
		}
		deleted, err := w.touched(tx, `DELETE FROM agents.lessons WHERE agent_id = $1::uuid`, foreign)
		if agAllowed(deleted, err) || deleted != 0 {
			t.Errorf("another person deleted %d lessons: %v", deleted, err)
		}
	})
	if err := w.owner.QueryRow(ctx, `SELECT count(*) FROM agents.lessons WHERE agent_id = $1::uuid`, foreign).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("lessons after the other person's delete = %d", n)
	}
}

func TestLessonRowSecurityByAgentAndAdminSession(t *testing.T) {
	w := newAgWorld(t)
	ctx := context.Background()
	agentA, agentB := w.own["owner"], w.own["admin"]
	var total int
	if err := w.owner.QueryRow(ctx, `SELECT count(*) FROM agents.lessons`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	var wantA int
	if err := w.owner.QueryRow(ctx, `SELECT count(*) FROM agents.lessons WHERE agent_id = $1::uuid`, agentA).Scan(&wantA); err != nil {
		t.Fatal(err)
	}
	if wantA != 1 || total <= wantA {
		t.Fatalf("planted lessons: agent A %d, all %d", wantA, total)
	}

	session := func(setting string, value string, fn func(tx pgx.Tx)) {
		tx, err := w.app.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT set_config($1, $2, true)`, setting, value); err != nil {
			t.Fatal(err)
		}
		fn(tx)
	}

	session("upmo.agent_id", agentA, func(tx pgx.Tx) {
		var n, other int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM agents.lessons`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM agents.lessons WHERE agent_id = $1::uuid`, agentB).Scan(&other); err != nil {
			t.Fatal(err)
		}
		if n != 1 || other != 0 {
			t.Errorf("agent session saw %d lessons, %d of another agent; want exactly its own 1", n, other)
		}
		inserted, err := w.touched(tx, `
			INSERT INTO agents.lessons (agent_id, title, cue, body) VALUES ($1::uuid, 'Inserted by agent', 'Cue', 'Body')`, agentA)
		if agAllowed(inserted, err) {
			t.Error("agent session inserted a lesson")
		}
		deleted, err := w.touched(tx, `DELETE FROM agents.lessons WHERE agent_id = $1::uuid`, agentA)
		if agAllowed(deleted, err) || deleted != 0 {
			t.Errorf("agent session deleted %d lessons: %v", deleted, err)
		}
	})
	var left int
	if err := w.owner.QueryRow(ctx, `SELECT count(*) FROM agents.lessons WHERE agent_id = $1::uuid`, agentA).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != wantA {
		t.Fatalf("lessons of agent A after the agent session's delete = %d, want %d", left, wantA)
	}

	session("upmo.lesson_access", "admin", func(tx pgx.Tx) {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM agents.lessons`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != total {
			t.Errorf("admin session saw %d lessons, want %d", n, total)
		}
	})
}
