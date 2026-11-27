//go:build integration

package chat_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
)

type stubProfiles struct {
	mu    sync.Mutex
	calls []string
	body  string
	err   error
}

func (s *stubProfiles) Profile(_ context.Context, userID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, userID)
	return s.body, s.err
}

func (s *stubProfiles) seenCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

type recordingProfiles struct {
	inner app.ProfileLookup
	mu    sync.Mutex
	calls []string
}

func (p *recordingProfiles) Profile(ctx context.Context, userID string) (string, error) {
	p.mu.Lock()
	p.calls = append(p.calls, userID)
	p.mu.Unlock()
	return p.inner.Profile(ctx, userID)
}

func (p *recordingProfiles) seenCalls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

func rebuildWorkerWithProfiles(t *testing.T, r *rig, profiles app.ProfileLookup) {
	t.Helper()
	deps := r.deps
	deps.Profiles = profiles
	worker, err := app.NewReplyWorker(deps)
	if err != nil {
		t.Fatal(err)
	}
	r.deps = deps
	r.worker = worker
}

func insertShareUsers(t *testing.T, r *rig) {
	t.Helper()
	for _, u := range []struct{ id, email, role string }{
		{ownerUser, "owner@example.com", "owner"},
		{otherUser, "other@example.com", "member"},
	} {
		if _, err := r.owner.Exec(context.Background(), `
			INSERT INTO identity.users (id, email, email_hash, email_hash_key_id, name, role_id, joined_at)
			VALUES ($1::uuid, $2, sha256(convert_to($2, 'UTF8')), 'k1', $2, (SELECT id FROM identity.roles WHERE key = $3), now())
			ON CONFLICT (id) DO NOTHING`, u.id, u.email, u.role); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReplyContextBreakSystemInPrivateChat(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	ctx := context.Background()
	if _, err := r.owner.Exec(ctx, `UPDATE identity.workspace SET name = 'Acme Corp', time_zone = 'Europe/Berlin' WHERE id = true`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO memory.profiles (user_id, body, updated_at)
		VALUES ($1::uuid, 'Ada prefers concise answers.', $2)`, ownerUser, time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	agent := r.agent(ownerAccess, agents.AgentFields{
		Name:         "Research",
		Instructions: "Answer as a researcher.",
		Enabled:      true,
	})
	lessonID, err := agents.SaveLesson(ctx, r.agents, agentsAccess(ownerAccess), agent.ID, agents.LessonFields{
		Title: "Tone",
		Cue:   "when writing",
		Body:  "Be plain.",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	chatID := r.open()
	if err := app.Say(ctx, r.store, ownerAccess, chatID, "turn 1", app.Picks{AgentID: agent.ID}); err != nil {
		t.Fatal(err)
	}
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.doneCalls("m1", 1, 1, nil, stubTool{ID: "c1", Name: "open_lesson", Args: fmt.Sprintf(`{"lesson_id":%q}`, lessonID)})
			return
		}
		s.delta("ok")
		s.done("m1", 1, 1, nil)
	})
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}

	if err := app.Say(ctx, r.store, ownerAccess, chatID, "turn 2", app.Picks{AgentID: agent.ID}); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}

	calls := r.stub.seen()
	if len(calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(calls))
	}
	got := calls[0].System
	for _, want := range []string{
		app.StandingRules,
		"Workspace: Acme Corp\nTime zone: Europe/Berlin",
		"# Agent: Research\n\nAnswer as a researcher.",
		"## House rules index\n\n- [Tone -> when writing] (id: " + lessonID + ")",
		"Profile:\nAda prefers concise answers.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("system missing %q:\n%s", want, got)
		}
	}
	if calls[1].System != got {
		t.Fatalf("round 2 system differed from round 1:\nr1=%q\nr2=%q", got, calls[1].System)
	}
	if calls[2].System != got {
		t.Fatalf("turn 2 system differed from turn 1:\nt1=%q\nt2=%q", got, calls[2].System)
	}

	var privateRead, tainted bool
	if err := r.owner.QueryRow(ctx, `SELECT private_read, tainted FROM chat.chats WHERE id = $1::uuid`, chatID).Scan(&privateRead, &tainted); err != nil {
		t.Fatal(err)
	}
	if !privateRead || tainted {
		t.Fatalf("exposure = (private_read=%v, tainted=%v), want (true, false)", privateRead, tainted)
	}
}

func TestReplyContextSharedChatExcludesProfile(t *testing.T) {
	t.Run("team visibility excludes profile without calling Profiles", func(t *testing.T) {
		r := newRig(t, app.ReplyConfig{})
		ctx := context.Background()
		stubProf := &stubProfiles{body: "Ada prefers concise answers."}
		rebuildWorkerWithProfiles(t, r, stubProf)

		const teamID = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaa99"
		owner := ownerAccess
		owner.Teams = "{" + teamID + "}"
		r.setAccess(ownerUser, owner)

		chatID := r.open()
		if _, err := r.owner.Exec(ctx, `UPDATE chat.chats SET visibility = 'team', visibility_team_id = $2::uuid WHERE id = $1::uuid`, chatID, teamID); err != nil {
			t.Fatal(err)
		}
		if err := app.Say(ctx, r.store, owner, chatID, "hello team", app.Picks{}); err != nil {
			t.Fatal(err)
		}
		r.answer()
		if err := r.run(r.lastJob(), 1); err != nil {
			t.Fatal(err)
		}

		if calls := stubProf.seenCalls(); len(calls) != 0 {
			t.Fatalf("Profiles called %v, want never", calls)
		}
		got := r.stub.seen()[0].System
		if strings.Contains(got, "Profile:") || strings.Contains(got, "Ada prefers concise answers.") {
			t.Fatalf("team chat system leaked profile:\n%s", got)
		}
		var privateRead bool
		if err := r.owner.QueryRow(ctx, `SELECT private_read FROM chat.chats WHERE id = $1::uuid`, chatID).Scan(&privateRead); err != nil {
			t.Fatal(err)
		}
		if privateRead {
			t.Fatalf("private_read = true on team chat, want false")
		}
	})

	t.Run("identity.shares row excludes profile without calling Profiles", func(t *testing.T) {
		r := newRig(t, app.ReplyConfig{})
		ctx := context.Background()
		stubProf := &stubProfiles{body: "Ada prefers concise answers."}
		rebuildWorkerWithProfiles(t, r, stubProf)
		insertShareUsers(t, r)

		chatID := r.open()
		if _, err := r.owner.Exec(ctx, `
			INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by)
			VALUES ('chat', $1::uuid, $2::uuid, 'viewer', $3::uuid)`,
			chatID, otherUser, ownerUser); err != nil {
			t.Fatal(err)
		}
		r.say(chatID, "hello shared", "")
		r.answer()
		if err := r.run(r.lastJob(), 1); err != nil {
			t.Fatal(err)
		}

		if calls := stubProf.seenCalls(); len(calls) != 0 {
			t.Fatalf("Profiles called %v, want never", calls)
		}
		got := r.stub.seen()[0].System
		if strings.Contains(got, "Profile:") || strings.Contains(got, "Ada prefers concise answers.") {
			t.Fatalf("shared chat system leaked profile:\n%s", got)
		}
		var privateRead bool
		if err := r.owner.QueryRow(ctx, `SELECT private_read FROM chat.chats WHERE id = $1::uuid`, chatID).Scan(&privateRead); err != nil {
			t.Fatal(err)
		}
		if privateRead {
			t.Fatalf("private_read = true on shared chat, want false")
		}
	})

	t.Run("share added after round 1 injected profile stops round 2 profile and keeps private_read true", func(t *testing.T) {
		r := newRig(t, app.ReplyConfig{})
		ctx := context.Background()
		recProf := &recordingProfiles{inner: r.deps.Profiles}
		rebuildWorkerWithProfiles(t, r, recProf)
		insertShareUsers(t, r)

		if _, err := r.owner.Exec(ctx, `
			INSERT INTO memory.profiles (user_id, body, updated_at)
			VALUES ($1::uuid, 'Ada prefers concise answers.', $2)`,
			ownerUser, time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
		agent := r.agent(ownerAccess, agents.AgentFields{Name: "Research", Enabled: true})
		lessonID, err := agents.SaveLesson(ctx, r.agents, agentsAccess(ownerAccess), agent.ID, agents.LessonFields{
			Title: "Tone",
			Cue:   "when writing",
			Body:  "Be plain.",
		}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		chatID := r.open()
		if err := app.Say(ctx, r.store, ownerAccess, chatID, "round 1 private", app.Picks{AgentID: agent.ID}); err != nil {
			t.Fatal(err)
		}
		r.stub.reply(func(call int, s *frames) {
			if call == 1 {
				s.doneCalls("m1", 1, 1, nil, stubTool{ID: "c1", Name: "open_lesson", Args: fmt.Sprintf(`{"lesson_id":%q}`, lessonID)})
				return
			}
			s.delta("ok")
			s.done("m1", 1, 1, nil)
		})
		if err := r.run(r.lastJob(), 1); err != nil {
			t.Fatal(err)
		}

		if calls := r.stub.seen(); len(calls) != 1 || !strings.Contains(calls[0].System, "Profile:\nAda prefers concise answers.") {
			t.Fatalf("round 1 system missing profile: %+v", calls)
		}
		if got := recProf.seenCalls(); len(got) != 1 || got[0] != ownerUser {
			t.Fatalf("round 1 Profiles calls = %v, want [%s]", got, ownerUser)
		}

		if _, err := r.owner.Exec(ctx, `
			INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by)
			VALUES ('chat', $1::uuid, $2::uuid, 'viewer', $3::uuid)`,
			chatID, otherUser, ownerUser); err != nil {
			t.Fatal(err)
		}
		if err := r.run(r.lastJob(), 1); err != nil {
			t.Fatal(err)
		}

		if got := recProf.seenCalls(); len(got) != 1 {
			t.Fatalf("Profiles called again in shared round 2: %v", got)
		}
		calls := r.stub.seen()
		if len(calls) != 2 {
			t.Fatalf("gateway calls = %d, want 2", len(calls))
		}
		if strings.Contains(calls[1].System, "Profile:") || strings.Contains(calls[1].System, "Ada prefers concise answers.") {
			t.Fatalf("round 2 after share still included profile:\n%s", calls[1].System)
		}
		var privateRead bool
		if err := r.owner.QueryRow(ctx, `SELECT private_read FROM chat.chats WHERE id = $1::uuid`, chatID).Scan(&privateRead); err != nil {
			t.Fatal(err)
		}
		if !privateRead {
			t.Fatalf("private_read = false after sharing, want true")
		}
		lines := r.lines(chatID)
		if last := lines[len(lines)-1]; last.Status != domain.StatusDone {
			t.Fatalf("reply status = %s, want done", last.Status)
		}
	})
}

func TestReplyContextProfileLookupErrorDoesNotFailReply(t *testing.T) {
	t.Run("lookup error still completes reply without profile and leaves private_read false", func(t *testing.T) {
		r := newRig(t, app.ReplyConfig{})
		ctx := context.Background()
		stubProf := &stubProfiles{err: errors.New("profile store unavailable")}
		rebuildWorkerWithProfiles(t, r, stubProf)

		chatID := r.open()
		r.say(chatID, "hello despite profile error", "")
		r.answer()
		if err := r.run(r.lastJob(), 1); err != nil {
			t.Fatalf("run failed on profile lookup error: %v", err)
		}

		if got := stubProf.seenCalls(); len(got) != 1 || got[0] != ownerUser {
			t.Fatalf("Profiles calls = %v, want [%s]", got, ownerUser)
		}
		lines := r.lines(chatID)
		if last := lines[len(lines)-1]; last.Status != domain.StatusDone || last.Body != "ok" {
			t.Fatalf("reply = %+v, want done with body ok", last)
		}
		got := r.stub.seen()[0].System
		if !strings.Contains(got, app.StandingRules) {
			t.Fatalf("system missing StandingRules:\n%s", got)
		}
		if strings.Contains(got, "Profile:") {
			t.Fatalf("system unexpectedly contained Profile:\n%s", got)
		}
		var privateRead bool
		if err := r.owner.QueryRow(ctx, `SELECT private_read FROM chat.chats WHERE id = $1::uuid`, chatID).Scan(&privateRead); err != nil {
			t.Fatal(err)
		}
		if privateRead {
			t.Fatalf("private_read = true on lookup error, want false")
		}
	})

	t.Run("blank profile omits profile block and leaves private_read false", func(t *testing.T) {
		r := newRig(t, app.ReplyConfig{})
		ctx := context.Background()
		if _, err := r.owner.Exec(ctx, `
			INSERT INTO memory.profiles (user_id, body, updated_at)
			VALUES ($1::uuid, '   ', $2)`,
			ownerUser, time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
		chatID := r.open()
		r.say(chatID, "hello blank profile", "")
		r.answer()
		if err := r.run(r.lastJob(), 1); err != nil {
			t.Fatal(err)
		}

		lines := r.lines(chatID)
		if last := lines[len(lines)-1]; last.Status != domain.StatusDone || last.Body != "ok" {
			t.Fatalf("reply = %+v, want done with body ok", last)
		}
		if got := r.stub.seen()[0].System; strings.Contains(got, "Profile:") {
			t.Fatalf("system contained Profile for blank body:\n%s", got)
		}
		var privateRead bool
		if err := r.owner.QueryRow(ctx, `SELECT private_read FROM chat.chats WHERE id = $1::uuid`, chatID).Scan(&privateRead); err != nil {
			t.Fatal(err)
		}
		if privateRead {
			t.Fatalf("private_read = true for blank profile, want false")
		}
	})
}

func TestReplyContextProfileRLSPerAuthor(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	ctx := context.Background()
	recProf := &recordingProfiles{inner: r.deps.Profiles}
	rebuildWorkerWithProfiles(t, r, recProf)

	const (
		adaProfile = "Ada prefers concise answers."
		bobProfile = "Bob prefers detailed breakdowns."
	)
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO memory.profiles (user_id, body, updated_at)
		VALUES ($1::uuid, $2, $4), ($3::uuid, $5, $4)`,
		ownerUser, adaProfile, otherUser, now, bobProfile); err != nil {
		t.Fatal(err)
	}

	r.answer()
	ownerChat := r.open()
	if err := app.Say(ctx, r.store, ownerAccess, ownerChat, "hi from Ada", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}

	otherOpened, err := app.Open(ctx, r.store, otherAccess)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, r.store, otherAccess, otherOpened.ID, "hi from Bob", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}

	recorded := recProf.seenCalls()
	if len(recorded) != 2 || recorded[0] != ownerUser || recorded[1] != otherUser {
		t.Errorf("Profiles called with user IDs %v, want [%s %s]", recorded, ownerUser, otherUser)
	}
	calls := r.stub.seen()
	if len(calls) != 2 {
		t.Fatalf("gateway calls = %d, want 2", len(calls))
	}
	if !strings.Contains(calls[0].System, "Profile:\n"+adaProfile) || strings.Contains(calls[0].System, bobProfile) {
		t.Errorf("owner system got wrong profile:\n%s", calls[0].System)
	}
	if !strings.Contains(calls[1].System, "Profile:\n"+bobProfile) || strings.Contains(calls[1].System, adaProfile) {
		t.Errorf("other user system got wrong profile:\n%s", calls[1].System)
	}
}
