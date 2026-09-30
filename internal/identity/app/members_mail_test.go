package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

var (
	mailActor  = mustUser("00000000-0000-7000-8000-000000000001")
	mailTarget = mustUser("00000000-0000-7000-8000-000000000002")
	mailRole   = mustRole("00000000-0000-7000-8000-000000000010")
	mailNow    = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
)

func TestInviteEmailsTheInviteLinkToTheInvitedPerson(t *testing.T) {
	sender := &fakeSender{}
	members := mailMembers(&mailStore{}, sender, nil)
	_, token, err := members.Invite(context.Background(), mailSession(), " ada@example.com ", "Ada", mailRole)
	if err != nil {
		t.Fatal(err)
	}
	want := sent{"invite", "ada@example.com", "https://upmo.test/login/link#" + token}
	if token == "" || len(sender.sent) != 1 || sender.sent[0] != want {
		t.Fatalf("sent = %+v, want %+v", sender.sent, want)
	}
}

func TestInviteLinkEmailsTheInviteLinkToTheStoredAddress(t *testing.T) {
	sender := &fakeSender{}
	members := mailMembers(&mailStore{invited: true, email: "bob@example.com"}, sender, nil)
	token, err := members.InviteLink(context.Background(), mailSession(), mailTarget)
	if err != nil {
		t.Fatal(err)
	}
	want := sent{"invite", "bob@example.com", "https://upmo.test/login/link#" + token}
	if len(sender.sent) != 1 || sender.sent[0] != want {
		t.Fatalf("sent = %+v, want %+v", sender.sent, want)
	}
}

func TestResetLinkEmailsTheRecoverLinkToTheStoredAddress(t *testing.T) {
	sender := &fakeSender{}
	members := mailMembers(&mailStore{email: "bob@example.com"}, sender, nil)
	token, err := members.ResetLink(context.Background(), mailSession(), mailTarget)
	if err != nil {
		t.Fatal(err)
	}
	want := sent{"recover", "bob@example.com", "https://upmo.test/login/link#" + token}
	if len(sender.sent) != 1 || sender.sent[0] != want {
		t.Fatalf("sent = %+v, want %+v", sender.sent, want)
	}
}

func TestRecoverEmailsTheRecoverLinkToTheStoredAddress(t *testing.T) {
	sender := &fakeSender{}
	members := mailMembers(&mailStore{known: true, email: "bob@example.com"}, sender, nil)
	token, err := members.Recover(context.Background(), "BOB@example.com")
	if err != nil {
		t.Fatal(err)
	}
	want := sent{"recover", "bob@example.com", "https://upmo.test/login/link#" + token}
	if len(sender.sent) != 1 || sender.sent[0] != want {
		t.Fatalf("sent = %+v, want %+v", sender.sent, want)
	}
}

func TestRecoverForAnUnknownEmailSendsNothingAndAnswersAsBefore(t *testing.T) {
	sender := &fakeSender{}
	members := mailMembers(&mailStore{}, sender, nil)
	_, err := members.Recover(context.Background(), "nobody@example.com")
	if !errors.Is(err, domain.ErrNoSuchMember) {
		t.Fatalf("err = %v", err)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sent = %+v", sender.sent)
	}
}

func TestRefusedLinkSendsNothing(t *testing.T) {
	sender := &fakeSender{}
	members := mailMembers(&mailStore{invited: false}, sender, nil)
	if _, err := members.InviteLink(context.Background(), mailSession(), mailTarget); !errors.Is(err, domain.ErrAlreadyJoined) {
		t.Fatalf("err = %v", err)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sent = %+v", sender.sent)
	}
}

func TestFailedSendStillReturnsTheTokenAndLogsWithoutIt(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	store := &mailStore{known: true, invited: true, email: "bob@example.com"}
	// The relay echoes the link back, so the warning must redact it.
	sender := &fakeSender{fail: true}
	members := mailMembers(store, sender, log)
	ctx := context.Background()

	_, invite, err := members.Invite(ctx, mailSession(), "ada@example.com", "Ada", mailRole)
	if err != nil || invite == "" {
		t.Fatalf("invite = %q, %v", invite, err)
	}
	inviteLink, err := members.InviteLink(ctx, mailSession(), mailTarget)
	if err != nil || inviteLink == "" {
		t.Fatalf("invite link = %q, %v", inviteLink, err)
	}
	recovered, err := members.Recover(ctx, "bob@example.com")
	if err != nil || recovered == "" {
		t.Fatalf("recover = %q, %v", recovered, err)
	}
	store.invited = false
	reset, err := members.ResetLink(ctx, mailSession(), mailTarget)
	if err != nil || reset == "" {
		t.Fatalf("reset = %q, %v", reset, err)
	}

	out := logs.String()
	if strings.Count(out, "link email not sent") != 4 || !strings.Contains(out, "level=WARN") {
		t.Fatalf("logs = %s", out)
	}
	for _, token := range []string{invite, inviteLink, recovered, reset} {
		if strings.Contains(out, token) {
			t.Fatalf("log holds a token: %s", out)
		}
	}
}

func TestMissingProfileStillReturnsTheToken(t *testing.T) {
	sender := &fakeSender{}
	store := &mailStore{email: "bob@example.com", profileErr: errors.New("gone")}
	token, err := mailMembers(store, sender, nil).ResetLink(context.Background(), mailSession(), mailTarget)
	if err != nil || token == "" {
		t.Fatalf("reset = %q, %v", token, err)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sent = %+v", sender.sent)
	}
}

func TestNilMailReturnsTheTokenWithoutSending(t *testing.T) {
	store := &mailStore{email: "bob@example.com"}
	members := Members{Store: store, Now: func() time.Time { return mailNow }}
	token, err := members.ResetLink(context.Background(), mailSession(), mailTarget)
	if err != nil || token == "" {
		t.Fatalf("reset = %q, %v", token, err)
	}
	if store.profiles != 0 {
		t.Fatalf("profile reads = %d", store.profiles)
	}
}

func mailMembers(store *mailStore, sender *fakeSender, log *slog.Logger) Members {
	return Members{
		Store:     store,
		Now:       func() time.Time { return mailNow },
		Mail:      sender,
		PublicURL: "https://upmo.test",
		Log:       log,
	}
}

func mailSession() Session {
	return Session{UserID: mailActor, SignedIn: mailNow}
}

type sent struct {
	purpose, to, link string
}

type fakeSender struct {
	sent []sent
	fail bool
}

func (f *fakeSender) SendInvite(_ context.Context, to, link string) error {
	return f.record("invite", to, link)
}

func (f *fakeSender) SendRecover(_ context.Context, to, link string) error {
	return f.record("recover", to, link)
}

func (f *fakeSender) record(purpose, to, link string) error {
	if f.fail {
		return errors.New("550 relay refused " + link)
	}
	f.sent = append(f.sent, sent{purpose, to, link})
	return nil
}

// mailStore is an owner managing one other person.
type mailStore struct {
	allowFake
	known      bool
	invited    bool
	email      string
	profileErr error
	profiles   int
}

func (f *mailStore) MemberActor(context.Context, domain.UserID) (domain.RoleID, string, bool, []string, error) {
	return domain.RoleID{}, "owner", true, nil, nil
}
func (f *mailStore) RoleSystem(context.Context, domain.RoleID) (string, error) {
	return "member", nil
}
func (f *mailStore) UserByEmail(context.Context, string) (domain.UserID, bool, error) {
	return mailTarget, f.known, nil
}
func (f *mailStore) InviteMember(_ context.Context, email, name string, role domain.RoleID, _, _ []byte, _, _ time.Time) (domain.Member, error) {
	return domain.Member{ID: mailTarget, Email: email, Name: name, RoleID: role, Invited: true}, nil
}
func (f *mailStore) ReplaceMemberLink(_ context.Context, _ domain.UserID, _ string, _ []byte, _ time.Time, rule func(string, bool) error) error {
	if rule == nil {
		return nil
	}
	return rule("member", f.invited)
}
func (f *mailStore) Profile(context.Context, domain.UserID) (string, string, error) {
	f.profiles++
	return f.email, "Bob", f.profileErr
}

func mustUser(s string) domain.UserID {
	id, err := domain.ParseUserID(s)
	if err != nil {
		panic(err)
	}
	return id
}

func mustRole(s string) domain.RoleID {
	id, err := domain.ParseRoleID(s)
	if err != nil {
		panic(err)
	}
	return id
}
