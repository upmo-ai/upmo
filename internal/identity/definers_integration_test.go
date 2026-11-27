//go:build integration

package identity_test

import (
	"context"
	"crypto/rand"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// definerWorld is the cast the actor definer tests share: a joined owner
// with a live session, and the sessions that must be refused.
type definerWorld struct {
	owner, app *pgxpool.Pool
	ctx        context.Context
	ownerID    string
	ownerSess  []byte
	// bad are the hashes no definer may accept, by name.
	bad map[string][]byte
}

func newDefinerWorld(t *testing.T) *definerWorld {
	t.Helper()
	owner, ctx := upOwner(t)
	w := &definerWorld{owner: owner, app: pgtest.AppLogin(t, owner), ctx: ctx}
	ownerRole := w.roleID(t, "owner")
	adminRole := w.roleID(t, "admin")
	w.ownerID = w.person(t, "ada@example.com", "Ada", ownerRole, true, false)
	disabledID := w.person(t, "gone@example.com", "Gone", adminRole, true, true)
	unjoinedID := w.person(t, "new@example.com", "New", adminRole, false, false)

	now := time.Now()
	w.ownerSess = randomHash(t)
	seedSess(t, owner, ctx, w.ownerID, w.ownerSess, now.Add(time.Hour))
	expired, disabled, unjoined := randomHash(t), randomHash(t), randomHash(t)
	seedSess(t, owner, ctx, w.ownerID, expired, now.Add(-time.Minute))
	seedSess(t, owner, ctx, disabledID, disabled, now.Add(time.Hour))
	seedSess(t, owner, ctx, unjoinedID, unjoined, now.Add(time.Hour))
	w.bad = map[string][]byte{
		"null":     nil,
		"random":   randomHash(t),
		"expired":  expired,
		"disabled": disabled,
		"unjoined": unjoined,
	}
	return w
}

func (w *definerWorld) roleID(t *testing.T, key string) string {
	t.Helper()
	var id string
	if err := w.owner.QueryRow(w.ctx, `SELECT id::text FROM identity.roles WHERE key = $1`, key).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// person seeds a person with a display name and returns their id.
func (w *definerWorld) person(t *testing.T, email, name, roleID string, joined, disabled bool) string {
	t.Helper()
	id := seedPerson(t, w.owner, w.ctx, email, roleID, joined, disabled)
	if _, err := w.owner.Exec(w.ctx, `UPDATE identity.users SET name = $2 WHERE id = $1::uuid`, id, name); err != nil {
		t.Fatal(err)
	}
	return id
}

func (w *definerWorld) team(t *testing.T, name string) string {
	t.Helper()
	var id string
	if err := w.owner.QueryRow(w.ctx, `INSERT INTO identity.teams (name) VALUES ($1) RETURNING id::text`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (w *definerWorld) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := w.owner.QueryRow(w.ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func randomHash(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func (w *definerWorld) randomUUID(t *testing.T) string {
	t.Helper()
	var id string
	if err := w.owner.QueryRow(w.ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// Every actor definer refuses a hash with no live joined session, even when
// upmo.user_id names the owner, and changes nothing.
func TestActorDefinersRefuseABadHash(t *testing.T) {
	w := newDefinerWorld(t)
	team := w.team(t, "T1")
	calls := map[string]struct {
		sql  string
		args []any
	}{
		"names_of": {`SELECT * FROM identity.names_of($1, $2::uuid[], $3::uuid[])`, []any{[]string{w.ownerID}, []string{team}}},
	}
	shares, members := w.count(t, "identity.shares"), w.count(t, "identity.team_members")
	for fn, call := range calls {
		for name, hash := range w.bad {
			tx, err := w.app.Begin(w.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(w.ctx, `SELECT set_config('upmo.user_id', $1, true)`, w.ownerID); err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(w.ctx, call.sql, append([]any{hash}, call.args...)...)
			_ = tx.Rollback(w.ctx)
			assertDenied(t, fn+" "+name, err)
		}
	}
	if got := w.count(t, "identity.shares"); got != shares {
		t.Fatalf("shares = %d, want %d unchanged", got, shares)
	}
	if got := w.count(t, "identity.team_members"); got != members {
		t.Fatalf("team_members = %d, want %d unchanged", got, members)
	}
}

// names_of answers only the enabled people and the teams asked for, with no
// other column, and Members.NamesOf gives the same maps.
func TestNamesOfReturnsOnlyAskedNames(t *testing.T) {
	w := newDefinerWorld(t)
	bob := w.person(t, "bob@example.com", "Bob", w.roleID(t, "member"), true, true)
	t1 := w.team(t, "T1")
	w.team(t, "T2")
	strangerPerson, strangerTeam := w.randomUUID(t), w.randomUUID(t)

	rows, err := w.app.Query(w.ctx, `SELECT * FROM identity.names_of($1, $2::uuid[], $3::uuid[])`,
		w.ownerSess, []string{w.ownerID, bob, strangerPerson}, []string{t1, strangerTeam})
	if err != nil {
		t.Fatal(err)
	}
	var columns []string
	for _, f := range rows.FieldDescriptions() {
		columns = append(columns, f.Name)
	}
	got := map[string]string{}
	for rows.Next() {
		var kind, id, name string
		if err := rows.Scan(&kind, &id, &name); err != nil {
			t.Fatal(err)
		}
		got[kind+" "+id] = name
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(columns) != 3 || columns[0] != "kind" || columns[1] != "id" || columns[2] != "name" {
		t.Fatalf("columns = %v, want kind, id, name", columns)
	}
	want := map[string]string{"person " + w.ownerID: "Ada", "team " + t1: "T1"}
	if !maps.Equal(got, want) {
		t.Fatalf("names_of = %v, want %v", got, want)
	}

	members := identity.Members(w.app, nil, "", nil, nil)
	people, teams, err := members.NamesOf(w.ctx, w.ownerSess, []string{w.ownerID, bob, strangerPerson}, []string{t1, strangerTeam})
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(people, map[string]string{w.ownerID: "Ada"}) {
		t.Fatalf("people = %v, want only Ada", people)
	}
	if !maps.Equal(teams, map[string]string{t1: "T1"}) {
		t.Fatalf("teams = %v, want only T1", teams)
	}

	// The store maps a refused actor to ErrNotSignedIn.
	if _, _, err := members.NamesOf(w.ctx, w.bad["random"], []string{w.ownerID}, nil); !errors.Is(err, identity.ErrNotSignedIn) {
		t.Fatalf("NamesOf with a random hash = %v, want ErrNotSignedIn", err)
	}
}
