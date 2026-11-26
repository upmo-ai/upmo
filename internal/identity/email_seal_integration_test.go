//go:build integration

package identity_test

// A person's email address is sealed with the master keyring and found
// through keyed index hashes, so a database dump holds no address. These tests
// read the stored row through the owner pool and prove every flow that writes
// or finds an address works on the sealed form, across a key rotation, and
// refuses plain values.

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/identity/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
	"github.com/upmo-ai/upmo/internal/testutil/workerkeys"
)

// sealedForm is the stored shape of a sealed address: key id, wrap nonce,
// wrapped key, payload nonce and payload.
var sealedForm = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}:[^:]+:[^:]+:[^:]+:[^:]+$`)

const (
	emailPassword = "correct horse battery"
	keyOneByte    = 'e'
	keyTwoByte    = 'f'
)

// emailRing builds a master keyring with current key id and byte, plus
// previous keys by id.
func emailRing(t *testing.T, id string, b byte, previous map[string]byte) gateway.Keyring {
	t.Helper()
	env := map[string]string{
		gateway.EnvMasterKey:   keyText(b),
		gateway.EnvMasterKeyID: id,
	}
	var environ []string
	for prevID, prevByte := range previous {
		environ = append(environ, gateway.EnvMasterKeyPrevious+prevID+"="+keyText(prevByte))
	}
	keys, err := gateway.KeyringFromEnv(func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}, environ)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func keyText(b byte) string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = b
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// emailKit is the identity use cases over one database, reachable as both the
// owner and upmo_app, with services built per email sealer.
type emailKit struct {
	owner    *pgxpool.Pool
	pool     *pgxpool.Pool
	sessions app.Service
	ctx      context.Context
}

func newEmailKit(t *testing.T) *emailKit {
	t.Helper()
	owner, pool, ctx := upBoth(t)
	return &emailKit{owner: owner, pool: pool, sessions: identity.Sessions(pool, domain.IdleTimeout, domain.MaxAge), ctx: ctx}
}

func (k *emailKit) members(emails identity.EmailSealer) app.Members {
	return identity.Members(k.pool, emails, "https://upmo.test", nil, nil)
}

// passwords is the password use case over ring's email sealer and a real
// worker holding the same ring, so both find a person by the same index.
func (k *emailKit) passwords(t *testing.T, ring gateway.Keyring) app.Passwords {
	t.Helper()
	passwords := identity.Passwords(k.pool, brainops.NewEmailSealer(ring), k.sessions)
	passwords.Keys = sealer.PasswordsOn(t, workerkeys.Start(t, k.owner, ring))
	return passwords
}

func (k *emailKit) passkeys(t *testing.T, emails identity.EmailSealer) app.Passkeys {
	t.Helper()
	passkeys, err := identity.Passkeys(k.pool, emails, k.sessions, "http://localhost:5173", "localhost", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return passkeys
}

// workerEmails is brain's email sealer over a real worker holding ring.
func (k *emailKit) workerEmails(t *testing.T, ring gateway.Keyring) brainops.WorkerEmails {
	t.Helper()
	return sealer.WorkerEmailOn(t, workerkeys.Start(t, k.owner, ring))
}

// profile is user's address and name as the password page reads them: the
// worker opens the address for a live session of user's.
func (k *emailKit) profile(t *testing.T, emails identity.EmailSealer, user domain.UserID) (string, string, error) {
	t.Helper()
	return k.store(emails).Profile(k.ctx, user, k.session(t, user).TokenHash)
}

func (k *emailKit) store(emails identity.EmailSealer) postgres.Passkeys {
	return postgres.Passkeys{Pool: k.pool, Emails: emails}
}

// ownerSession seeds a joined owner sealed by emails and starts their session.
func (k *emailKit) ownerSession(t *testing.T, emails identity.EmailSealer, email string) app.Session {
	t.Helper()
	return k.session(t, parseUser(t, seedSealedOwner(t, k.owner, k.ctx, emails, email)))
}

func (k *emailKit) session(t *testing.T, user domain.UserID) app.Session {
	t.Helper()
	_, session, err := k.sessions.Start(k.ctx, user, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func (k *emailKit) role(t *testing.T, key string) domain.RoleID {
	t.Helper()
	var id string
	if err := k.owner.QueryRow(k.ctx, `SELECT id::text FROM identity.roles WHERE key = $1`, key).Scan(&id); err != nil {
		t.Fatal(err)
	}
	role, err := domain.ParseRoleID(id)
	if err != nil {
		t.Fatal(err)
	}
	return role
}

// row is the stored email columns of one person.
type row struct {
	email   string
	hash    []byte
	keyID   string
	hashLen int
}

func (k *emailKit) row(t *testing.T, user domain.UserID) row {
	t.Helper()
	var r row
	if err := k.owner.QueryRow(k.ctx, `
		SELECT email, email_hash, email_hash_key_id, octet_length(email_hash)
		FROM identity.users WHERE id = $1`, user).Scan(&r.email, &r.hash, &r.keyID, &r.hashLen); err != nil {
		t.Fatal(err)
	}
	return r
}

func (k *emailKit) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := k.owner.QueryRow(k.ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// assertSealed fails unless r holds address sealed: no casing of it in the
// stored value, the sealed shape and a 32-byte index hash.
func assertSealed(t *testing.T, r row, address string) {
	t.Helper()
	if strings.Contains(strings.ToLower(r.email), strings.ToLower(address)) {
		t.Fatalf("stored email %q carries the address", r.email)
	}
	if !sealedForm.MatchString(r.email) {
		t.Fatalf("stored email %q, want <key id>:<nonce>:<ciphertext>", r.email)
	}
	if r.hashLen != 32 {
		t.Fatalf("email_hash is %d bytes, want 32", r.hashLen)
	}
}

// assertQuiet fails if err's text carries any of the secrets in any casing.
func assertQuiet(t *testing.T, err error, secrets ...string) {
	t.Helper()
	text := strings.ToLower(err.Error())
	for _, secret := range secrets {
		if strings.Contains(text, strings.ToLower(secret)) {
			t.Fatalf("error %q carries %q", err.Error(), secret)
		}
	}
}

func parseUser(t *testing.T, id string) domain.UserID {
	t.Helper()
	user, err := domain.ParseUserID(id)
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func emailsOf(members []domain.Member) []string {
	out := make([]string, len(members))
	for i, member := range members {
		out[i] = member.Email
	}
	return out
}

func TestEmailIsStoredSealed(t *testing.T) {
	k := newEmailKit(t)
	emails := sealer.WorkerEmail(t, k.owner)
	members := k.members(emails)
	session := k.ownerSession(t, emails, "owner@example.com")
	grace, _, err := members.Invite(k.ctx, session, "Grace.Hopper@Example.com", "Grace", k.role(t, "member"))
	if err != nil {
		t.Fatal(err)
	}
	r := k.row(t, grace.ID)
	assertSealed(t, r, "Grace.Hopper@Example.com")
	if r.keyID != "k1" {
		t.Fatalf("email_hash_key_id = %q, want k1", r.keyID)
	}
	email, _, err := k.profile(t, emails, grace.ID)
	if err != nil || email != "Grace.Hopper@Example.com" {
		t.Fatalf("profile %q %v, want the address with its casing", email, err)
	}
	listed, err := members.List(k.ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(emailsOf(listed), "Grace.Hopper@Example.com") {
		t.Fatalf("list = %v, want Grace.Hopper@Example.com", emailsOf(listed))
	}
}

func TestSealedEmailFlows(t *testing.T) {
	k := newEmailKit(t)
	emails := sealer.WorkerEmail(t, k.owner)
	members := k.members(emails)
	passwords := k.passwords(t, sealer.Ring(t))
	store := k.store(emails)
	if _, err := k.passkeys(t, emails).Bootstrap(k.ctx, "Ada@Example.com", "Ada"); err != nil {
		t.Fatal(err)
	}
	owner, found, err := store.UserByEmail(k.ctx, "ada@example.com")
	if err != nil || !found {
		t.Fatalf("bootstrapped owner by address %v %v", found, err)
	}
	assertSealed(t, k.row(t, owner), "Ada@Example.com")

	if err := passwords.Set(k.ctx, owner, "", "Ada@Example.com", "Ada", emailPassword); err != nil {
		t.Fatal(err)
	}
	if token, _, _, err := passwords.SignIn(k.ctx, "ADA@example.COM", emailPassword, "198.51.100.30", ""); err != nil || token == "" {
		t.Fatalf("sign-in in other casing %q %v", token, err)
	}
	// R12: the account names no address; only registration start asks the
	// worker for one.
	account, err := store.Account(k.ctx, owner)
	if err != nil || account.Name != "" || account.Display != "Ada" {
		t.Fatalf("passkey account %q %q %v, want no address and the name", account.Name, account.Display, err)
	}

	session := k.session(t, owner)
	role := k.role(t, "member")
	bob, _, err := members.Invite(k.ctx, session, "bob@example.com", "Bob", role)
	if err != nil {
		t.Fatal(err)
	}
	if err := members.Remove(k.ctx, session, bob.ID); err != nil {
		t.Fatal(err)
	}
	again, _, err := members.Invite(k.ctx, session, "Bob@Example.com", "Robert", role)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != bob.ID {
		t.Fatalf("re-invite id %s, want the disabled row %s", again.ID, bob.ID)
	}
	if n := k.count(t, `SELECT count(*) FROM identity.users`); n != 2 {
		t.Fatalf("users = %d, want 2", n)
	}
	if email, _, err := k.profile(t, emails, bob.ID); err != nil || email != "Bob@Example.com" {
		t.Fatalf("re-invited profile %q %v", email, err)
	}

	if id, found, err := store.UserByEmail(k.ctx, " BOB@example.com "); err != nil || !found || id != bob.ID {
		t.Fatalf("user by address %s %v %v, want %s", id, found, err, bob.ID)
	}
	if token, err := members.Recover(k.ctx, " ada@EXAMPLE.com "); err != nil || token == "" {
		t.Fatalf("recover %q %v", token, err)
	}
}

func TestProvisionWithSealedEmail(t *testing.T) {
	k := newEmailKit(t)
	emails := sealer.NewEmail(t)
	seedSealedOwner(t, k.owner, k.ctx, emails, "owner@example.com")
	prov := identity.Provision(k.owner, emails, "https://upmo.test")
	created, err := prov.CreateMember(k.ctx, identity.ProvisionMemberInput{
		ExternalID: "ext-grace", Email: "Grace@Example.com", Name: "Grace", Role: "member", KeyID: "k-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	grace := parseUser(t, created.ID)
	before := k.row(t, grace)
	assertSealed(t, before, "Grace@Example.com")

	if _, err := prov.UpdateMember(k.ctx, "ext-grace", identity.ProvisionMemberUpdate{
		Email: "grace.new@example.com", Name: "Grace", Role: "member", KeyID: "k-test",
	}); err != nil {
		t.Fatal(err)
	}
	after := k.row(t, grace)
	assertSealed(t, after, "grace.new@example.com")
	if after.email == before.email || bytes.Equal(after.hash, before.hash) {
		t.Fatal("changing the address kept the stored value or its hash")
	}
	if email, _, err := k.profile(t, sealer.WorkerEmail(t, k.owner), grace); err != nil || email != "grace.new@example.com" {
		t.Fatalf("profile %q %v, want the new address", email, err)
	}

	if _, err := prov.CreateMember(k.ctx, identity.ProvisionMemberInput{
		ExternalID: "ext-twin", Email: "GRACE.NEW@example.com", Name: "Twin", Role: "member", KeyID: "k-test",
	}); !errors.Is(err, identity.ErrEmailConflict) {
		t.Fatalf("create of a present address in other casing = %v, want ErrEmailConflict", err)
	}
}

func TestListMembersSortsByAddress(t *testing.T) {
	k := newEmailKit(t)
	emails := sealer.WorkerEmail(t, k.owner)
	members := k.members(emails)
	session := k.ownerSession(t, emails, "owner@example.com")
	role := k.role(t, "member")
	invited := map[string]domain.UserID{}
	for _, email := range []string{"carol@example.com", "Alice@example.com", "bob@Example.com"} {
		member, _, err := members.Invite(k.ctx, session, email, "Person", role)
		if err != nil {
			t.Fatal(err)
		}
		invited[email] = member.ID
		// Only a joined person can be on a team.
		if _, err := k.owner.Exec(k.ctx, `UPDATE identity.users SET joined_at = now() WHERE id = $1`, member.ID); err != nil {
			t.Fatal(err)
		}
	}
	for team, people := range map[string][]string{
		"Finance": {"Alice@example.com"},
		"Ops":     {"Alice@example.com", "carol@example.com"},
	} {
		var id string
		if err := k.pool.QueryRow(k.ctx, `INSERT INTO identity.teams (name) VALUES ($1) RETURNING id`, team).Scan(&id); err != nil {
			t.Fatal(err)
		}
		for _, person := range people {
			if _, err := k.pool.Exec(k.ctx, `INSERT INTO identity.team_members (team_id, user_id) VALUES ($1, $2)`, id, invited[person]); err != nil {
				t.Fatal(err)
			}
		}
	}
	listed, err := members.List(k.ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Alice@example.com", "bob@Example.com", "carol@example.com", "owner@example.com"}
	if got := emailsOf(listed); !slices.Equal(got, want) {
		t.Fatalf("list order = %v, want %v", got, want)
	}
	if !slices.Equal(listed[0].Teams, []string{"Finance", "Ops"}) || len(listed[1].Teams) != 0 || !slices.Equal(listed[2].Teams, []string{"Ops"}) {
		t.Fatalf("teams = %v %v %v", listed[0].Teams, listed[1].Teams, listed[2].Teams)
	}
}

func TestInviteSameAddressInOtherCasingConflicts(t *testing.T) {
	k := newEmailKit(t)
	emails := sealer.NewEmail(t)
	members := k.members(emails)
	session := k.ownerSession(t, emails, "owner@example.com")
	role := k.role(t, "member")
	if _, _, err := members.Invite(k.ctx, session, "ada@Example.com", "Ada", role); err != nil {
		t.Fatal(err)
	}
	if _, _, err := members.Invite(k.ctx, session, "ADA@example.com", "Ada", role); !errors.Is(err, domain.ErrAlreadyMember) {
		t.Fatalf("second invite = %v, want ErrAlreadyMember", err)
	}
	if n := k.count(t, `SELECT count(*) FROM identity.users WHERE email_hash = ANY($1)`, sealer.Hashes(t, "ada@example.com")); n != 1 {
		t.Fatalf("rows for the address = %d, want 1", n)
	}
}

func TestEmailRotation(t *testing.T) {
	k := newEmailKit(t)
	ringOne := emailRing(t, "k1", keyOneByte, nil)
	ringTwoOne := emailRing(t, "k2", keyTwoByte, map[string]byte{"k1": keyOneByte})
	ringTwo := emailRing(t, "k2", keyTwoByte, nil)
	one := k.workerEmails(t, ringOne)
	twoOne := k.workerEmails(t, ringTwoOne)
	two := k.workerEmails(t, ringTwo)
	quiet := []string{"grace@example.com", keyText(keyOneByte), keyText(keyTwoByte)}

	session := k.ownerSession(t, one, "owner@example.com")
	role := k.role(t, "member")
	grace, _, err := k.members(one).Invite(k.ctx, session, "Grace@Example.com", "Grace", role)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.owner.Exec(k.ctx, `UPDATE identity.users SET joined_at = now() WHERE id = $1`, grace.ID); err != nil {
		t.Fatal(err)
	}
	if err := k.passwords(t, ringOne).Set(k.ctx, grace.ID, "", "Grace@Example.com", "Grace", emailPassword); err != nil {
		t.Fatal(err)
	}
	if r := k.row(t, grace.ID); r.keyID != "k1" || !strings.HasPrefix(r.email, "k1:") {
		t.Fatalf("stored under %q (%q), want k1", r.keyID, r.email)
	}

	// k2 current, k1 previous: the row is found by its k1 index hash.
	if token, _, _, err := k.passwords(t, ringTwoOne).SignIn(k.ctx, "grace@example.com", emailPassword, "198.51.100.40", ""); err != nil || token == "" {
		t.Fatalf("sign-in after rotation %q %v", token, err)
	}
	if email, _, err := k.profile(t, twoOne, grace.ID); err != nil || email != "Grace@Example.com" {
		t.Fatalf("profile after rotation %q %v", email, err)
	}

	// k2 alone: the address is neither found nor opened.
	sessions := k.count(t, `SELECT count(*) FROM identity.sessions WHERE user_id = $1`, grace.ID)
	_, _, _, err = k.passwords(t, ringTwo).SignIn(k.ctx, "grace@example.com", emailPassword, "198.51.100.41", "")
	if !errors.Is(err, app.ErrWrong) {
		t.Fatalf("sign-in without k1 = %v, want ErrWrong", err)
	}
	assertQuiet(t, err, quiet...)
	if n := k.count(t, `SELECT count(*) FROM identity.sessions WHERE user_id = $1`, grace.ID); n != sessions {
		t.Fatalf("sessions %d -> %d, want no session started", sessions, n)
	}
	// The worker without k1 cannot open the address: a 500 to brain, which
	// is try again and never an address.
	email, _, err := k.profile(t, two, grace.ID)
	if !errors.Is(err, identity.ErrBusy) || email != "" {
		t.Fatalf("profile without k1 %q %v, want try again", email, err)
	}
	assertQuiet(t, err, quiet...)

	// A re-invite of the disabled person under k2 and k1 re-seals under k2.
	members := k.members(twoOne)
	if err := members.Remove(k.ctx, session, grace.ID); err != nil {
		t.Fatal(err)
	}
	again, _, err := members.Invite(k.ctx, session, "grace@example.com", "Grace", role)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != grace.ID {
		t.Fatalf("re-invite id %s, want %s", again.ID, grace.ID)
	}
	r := k.row(t, grace.ID)
	assertSealed(t, r, "grace@example.com")
	if r.keyID != "k2" || !strings.HasPrefix(r.email, "k2:") {
		t.Fatalf("re-invite stored under %q (%q), want k2", r.keyID, r.email)
	}
	if email, _, err := k.profile(t, two, grace.ID); err != nil || email != "grace@example.com" {
		t.Fatalf("profile under k2 alone after re-seal %q %v", email, err)
	}
}

func TestInviteRefusesAnAddressStoredUnderTwoKeys(t *testing.T) {
	k := newEmailKit(t)
	one := brainops.NewEmailSealer(emailRing(t, "k1", keyOneByte, nil))
	two := brainops.NewEmailSealer(emailRing(t, "k2", keyTwoByte, nil))
	twoOne := brainops.NewEmailSealer(emailRing(t, "k2", keyTwoByte, map[string]byte{"k1": keyOneByte}))
	session := k.ownerSession(t, one, "owner@example.com")
	role := k.role(t, "member")
	// Both rows are disabled, so a single match would be re-invited.
	for _, emails := range []identity.EmailSealer{one, two} {
		if _, err := k.owner.Exec(k.ctx, `
			INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, invited_at, disabled_at)
			VALUES ($1, $2, $3, 'Grace', (SELECT id FROM identity.roles WHERE key = 'member'), now(), now())`,
			sealUser(t, emails, "grace@example.com")...); err != nil {
			t.Fatal(err)
		}
	}
	const snapshot = `
		SELECT string_agg(concat_ws('|', id, email, encode(email_hash, 'hex'), email_hash_key_id, name, invited_at, disabled_at), ',' ORDER BY id)
		FROM identity.users`
	var before string
	if err := k.owner.QueryRow(k.ctx, snapshot).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, _, err := k.members(twoOne).Invite(k.ctx, session, "grace@example.com", "Grace", role); !errors.Is(err, domain.ErrAlreadyMember) {
		t.Fatalf("invite of an address under k1 and k2 = %v, want ErrAlreadyMember", err)
	}
	var after string
	if err := k.owner.QueryRow(k.ctx, snapshot).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("users changed:\n%s\n->\n%s", before, after)
	}
}

func TestPlainEmailIsRefused(t *testing.T) {
	k := newEmailKit(t)
	emails := sealer.WorkerEmail(t, k.owner)
	session := k.ownerSession(t, emails, "owner@example.com")
	var id string
	if err := k.owner.QueryRow(k.ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, invited_at, joined_at)
		VALUES ('plain@example.com', sha256('plain'::bytea), 'k1', 'Plain',
		        (SELECT id FROM identity.roles WHERE key = 'member'), now(), now())
		RETURNING id::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	plain := parseUser(t, id)
	// The worker refuses to open the plain value (500); brain says try
	// again and shows no address. The account reads no address at all.
	_, _, profileErr := k.profile(t, emails, plain)
	_, listErr := k.members(emails).List(k.ctx, session)
	for what, err := range map[string]error{"profile": profileErr, "list": listErr} {
		if !errors.Is(err, identity.ErrBusy) {
			t.Fatalf("%s = %v, want try again", what, err)
		}
		assertQuiet(t, err, "plain@example.com", keyText('t'))
	}
	if account, err := k.store(emails).Account(k.ctx, plain); err != nil || account.Name != "" {
		t.Fatalf("account = %q %v, want no address", account.Name, err)
	}
}

func TestEmailWritesNeedASealer(t *testing.T) {
	for name, emails := range map[string]identity.EmailSealer{
		"nil":        nil,
		"empty ring": brainops.NewEmailSealer(gateway.Keyring{}),
	} {
		t.Run(name, func(t *testing.T) {
			k := newEmailKit(t)
			_, err := k.passkeys(t, emails).Bootstrap(k.ctx, "ada@example.com", "Ada")
			if !errors.Is(err, identity.ErrNoEmailSealer) {
				t.Fatalf("bootstrap = %v, want ErrNoEmailSealer", err)
			}
			assertQuiet(t, err, "ada@example.com")
			if n := k.count(t, `SELECT count(*) FROM identity.users`); n != 0 {
				t.Fatalf("bootstrap left %d rows", n)
			}

			session := k.ownerSession(t, sealer.NewEmail(t), "owner@example.com")
			_, _, err = k.members(emails).Invite(k.ctx, session, "bob@example.com", "Bob", k.role(t, "member"))
			if !errors.Is(err, identity.ErrNoEmailSealer) {
				t.Fatalf("invite = %v, want ErrNoEmailSealer", err)
			}
			assertQuiet(t, err, "bob@example.com")
			if n := k.count(t, `SELECT count(*) FROM identity.users`); n != 1 {
				t.Fatalf("invite left %d rows, want the owner only", n)
			}
		})
	}
}
