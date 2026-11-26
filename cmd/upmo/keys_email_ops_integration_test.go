//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/keys"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
)

// The people of the email-op suites, beside adaID, bobID, carolID and daveID.
const (
	erinID  = "aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa"
	frankID = "cccccccc-cccc-7ccc-8ccc-cccccccccccc"
)

// emailOp is one email body as brain sends it: the op and its fields, each
// left out when nil or empty.
func emailOp(op string, actor, link []byte, user string) string {
	in := map[string]any{"op": op}
	if actor != nil {
		in["actor_token_hash"] = actor
	}
	if link != nil {
		in["link_hash"] = link
	}
	if user != "" {
		in["user_id"] = user
	}
	raw, _ := json.Marshal(in)
	return string(raw)
}

// email posts an email body as audience.
func (w *keyWorker) email(audience, body string) (int, string) {
	w.t.Helper()
	return w.call(audience, http.MethodPost, keys.PathEmail, body)
}

// link stores a sign-in link for user that ends after lifetime, spent when
// used, and returns its token hash.
func (w *keyWorker) link(user string, lifetime time.Duration, used bool) []byte {
	w.t.Helper()
	hash := make([]byte, 32)
	if _, err := rand.Read(hash); err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.owner.Exec(context.Background(), `
		INSERT INTO identity.sign_in_links (token_hash, user_id, purpose, expires_at, used_at)
		VALUES ($1, $2, 'recover', now() + make_interval(secs => $3), CASE WHEN $4 THEN now() END)`,
		hash, user, lifetime.Seconds(), used); err != nil {
		w.t.Fatal(err)
	}
	return hash
}

// unjoin marks user as invited and not yet joined.
func (w *keyWorker) unjoin(user string) {
	w.t.Helper()
	if _, err := w.owner.Exec(context.Background(), `UPDATE identity.users SET joined_at = NULL, invited_at = now() WHERE id = $1`, user); err != nil {
		w.t.Fatal(err)
	}
}

// refusedQuietly fails unless the answer is the one 403 body, with no
// address in it.
func refusedQuietly(t *testing.T, what string, code int, body string) {
	t.Helper()
	if code != http.StatusForbidden || body != "keys: refused" || strings.Contains(body, "@") {
		t.Errorf("%s = %d %q, want 403 keys: refused", what, code, body)
	}
}

func address(a string) string { return `{"address":"` + a + `"}` + "\n" }

// Invariant 1: the email route takes only brain's audience and, for each
// op, exactly its own fields at their own sizes; anything else is the same
// 403 with no address in it.
func TestKeysEmailRefusals(t *testing.T) {
	w := startKeyWorker(t)
	w.person(adaID, "Ada@Example.com", "owner", "", false)
	w.person(bobID, "Bob@Example.com", "member", "", false)
	w.person(erinID, "Erin@Example.com", "member", "", false)
	ada := w.session(adaID, time.Hour)
	link := w.link(bobID, time.Hour, false)
	short := ada[:31]
	if code, body := w.email(svcsig.AudienceWorkerBrain, emailOp("member", ada, nil, erinID)); code != http.StatusOK || body != address("Erin@Example.com") {
		t.Fatalf("member with a lower-case uuid = %d %q", code, body)
	}

	// Control: each well-formed op answers.
	if code, body := w.email(svcsig.AudienceWorkerBrain, emailOp("own", ada, nil, "")); code != http.StatusOK || body != address("Ada@Example.com") {
		t.Fatalf("own = %d %q", code, body)
	}
	if code, body := w.email(svcsig.AudienceWorkerBrain, emailOp("link", nil, link, "")); code != http.StatusOK || body != address("Bob@Example.com") {
		t.Fatalf("link = %d %q", code, body)
	}
	if code, body := w.email(svcsig.AudienceWorkerBrain, emailOp("member", ada, nil, bobID)); code != http.StatusOK || body != address("Bob@Example.com") {
		t.Fatalf("member = %d %q", code, body)
	}

	b64 := base64.StdEncoding.EncodeToString(ada)
	for name, body := range map[string]string{
		"unknown op":              emailOp("passkey", ada, nil, ""),
		"no op":                   `{"actor_token_hash":"` + b64 + `"}`,
		"an unknown field":        `{"op":"own","actor_token_hash":"` + b64 + `","extra":1}`,
		"own with a link hash":    emailOp("own", ada, link, ""),
		"own with no hash":        emailOp("own", nil, nil, ""),
		"own with a null hash":    `{"op":"own","actor_token_hash":null}`,
		"own with user_id":        emailOp("own", ada, nil, bobID),
		"own with 31 bytes":       emailOp("own", short, nil, ""),
		"own with 33 bytes":       emailOp("own", append(bytes.Clone(ada), 1), nil, ""),
		"own with a bad base64":   `{"op":"own","actor_token_hash":"not base64!"}`,
		"link with a session":     emailOp("link", ada, nil, ""),
		"link with 31 bytes":      emailOp("link", nil, link[:31], ""),
		"link with user_id":       emailOp("link", nil, link, bobID),
		"directory with user_id":  emailOp("directory", ada, nil, bobID),
		"directory with a link":   emailOp("directory", ada, link, ""),
		"member with no user_id":  emailOp("member", ada, nil, ""),
		"member with no session":  emailOp("member", nil, nil, bobID),
		"member upper-case uuid":  emailOp("member", ada, nil, strings.ToUpper(erinID)),
		"member with a short id":  emailOp("member", ada, nil, "bob"),
		"member with a link hash": emailOp("member", ada, link, bobID),
		"two bodies":              emailOp("own", ada, nil, "") + emailOp("own", ada, nil, ""),
	} {
		code, got := w.email(svcsig.AudienceWorkerBrain, body)
		refusedQuietly(t, name, code, got)
	}
	for _, audience := range []string{svcsig.AudienceWorkerGateway, svcsig.AudienceWorkerSandboxd} {
		for _, body := range []string{emailOp("own", ada, nil, ""), emailOp("link", nil, link, ""), emailOp("member", ada, nil, bobID), emailOp("directory", ada, nil, "")} {
			code, got := w.email(audience, body)
			refusedQuietly(t, audience+" "+body, code, got)
		}
	}
}

// Invariant 2: own answers the address of the enabled person behind an
// unexpired session, and nobody else's.
func TestKeysEmailOwn(t *testing.T) {
	w := startKeyWorker(t)
	w.person(adaID, "Ada@Example.com", "owner", "", false)
	w.person(bobID, "Bob@Example.org", "member", "", false)
	w.person(carolID, "Carol@Example.com", "member", "", true)
	ada := w.session(adaID, time.Hour)
	bob := w.session(bobID, time.Hour)
	ended := w.session(adaID, -time.Second)
	disabled := w.session(carolID, time.Hour)

	if code, body := w.email(svcsig.AudienceWorkerBrain, emailOp("own", ada, nil, "")); code != http.StatusOK || body != address("Ada@Example.com") {
		t.Fatalf("own for Ada = %d %q", code, body)
	}
	if code, body := w.email(svcsig.AudienceWorkerBrain, emailOp("own", bob, nil, "")); code != http.StatusOK || body != address("Bob@Example.org") {
		t.Fatalf("own for Bob = %d %q", code, body)
	}
	for name, hash := range map[string][]byte{
		"an ended session":         ended,
		"a disabled person's":      disabled,
		"an unknown hash":          bytes.Repeat([]byte{7}, 32),
		"Ada's session reversed":   reversed(ada),
		"a link hash, not session": w.link(adaID, time.Hour, false),
	} {
		code, body := w.email(svcsig.AudienceWorkerBrain, emailOp("own", hash, nil, ""))
		refusedQuietly(t, "own with "+name, code, body)
	}
	// A session is not an API token: a live token's hash opens nothing.
	token := bytes.Repeat([]byte{9}, 32)
	if _, err := w.owner.Exec(context.Background(), `
		INSERT INTO identity.api_tokens (user_id, name, token_hash, scope, created_at, expires_at)
		VALUES ($1, 'cli', $2, ARRAY['audit:view'], now(), now() + interval '1 hour')`, adaID, token); err != nil {
		t.Fatal(err)
	}
	code, body := w.email(svcsig.AudienceWorkerBrain, emailOp("own", token, nil, ""))
	refusedQuietly(t, "own with an API token", code, body)
}

func reversed(b []byte) []byte {
	out := bytes.Clone(b)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Invariant 3: link answers the address of the enabled person an unused,
// unexpired sign-in link is for.
func TestKeysEmailLink(t *testing.T) {
	w := startKeyWorker(t)
	w.person(adaID, "Ada@Example.com", "owner", "", false)
	w.person(bobID, "Bob@Example.com", "member", "", false)
	w.person(carolID, "Carol@Example.com", "member", "", true)
	w.unjoin(bobID)

	if code, body := w.email(svcsig.AudienceWorkerBrain, emailOp("link", nil, w.link(bobID, time.Hour, false), "")); code != http.StatusOK || body != address("Bob@Example.com") {
		t.Fatalf("a live link for an invited Bob = %d %q", code, body)
	}
	for name, hash := range map[string][]byte{
		"a used link":                    w.link(adaID, time.Hour, true),
		"an expired link":                w.link(adaID, -time.Second, false),
		"a disabled person's live link":  w.link(carolID, time.Hour, false),
		"an unknown hash":                bytes.Repeat([]byte{7}, 32),
		"Ada's session hash, not a link": w.session(adaID, time.Hour),
		"a used link of an invited Bob":  w.link(bobID, time.Hour, true),
		"an expired link of invited Bob": w.link(bobID, -time.Minute, false),
	} {
		code, body := w.email(svcsig.AudienceWorkerBrain, emailOp("link", nil, hash, ""))
		refusedQuietly(t, "link with "+name, code, body)
	}
}

// Invariant 4: member answers one enabled person's address to a joined,
// enabled person with a live session who may manage that person's role.
func TestKeysEmailMember(t *testing.T) {
	w := startKeyWorker(t)
	w.person(adaID, "Ada@Example.com", "owner", "", false)
	w.person(bobID, "Bob@Example.com", "admin", "", false)
	w.person(carolID, "Carol@Example.com", "member", "", false)
	w.person(daveID, "Dave@Example.com", "member", "", true)
	w.person(erinID, "Erin@Example.com", "admin", "", false)
	w.person(frankID, "Frank@Example.com", "viewer", "", false)
	w.unjoin(erinID)
	w.unjoin(frankID)
	ada := w.session(adaID, time.Hour)
	admin := w.session(bobID, time.Hour)
	member := w.session(carolID, time.Hour)
	unjoined := w.session(erinID, time.Hour)
	ended := w.session(bobID, -time.Second)

	for _, tc := range []struct {
		name   string
		actor  []byte
		target string
		want   string
	}{
		{"an admin asks for a member", admin, carolID, "Carol@Example.com"},
		{"an admin asks for an invited viewer", admin, frankID, "Frank@Example.com"},
		{"an owner asks for an owner", ada, adaID, "Ada@Example.com"},
		{"an owner asks for an admin", ada, bobID, "Bob@Example.com"},
	} {
		if code, body := w.email(svcsig.AudienceWorkerBrain, emailOp("member", tc.actor, nil, tc.target)); code != http.StatusOK || body != address(tc.want) {
			t.Errorf("%s = %d %q, want %s", tc.name, code, body, tc.want)
		}
	}
	for _, tc := range []struct {
		name   string
		actor  []byte
		target string
	}{
		{"an admin asks for an owner", admin, adaID},
		{"a member asks for a member", member, carolID},
		{"a member asks for an admin", member, bobID},
		{"an admin asks for a disabled member", admin, daveID},
		{"an owner asks for a disabled member", ada, daveID},
		{"an admin asks for nobody", admin, "99999999-9999-7999-8999-999999999999"},
		{"an unjoined admin asks for a member", unjoined, carolID},
		{"an ended admin session asks for a member", ended, carolID},
	} {
		code, body := w.email(svcsig.AudienceWorkerBrain, emailOp("member", tc.actor, nil, tc.target))
		refusedQuietly(t, tc.name, code, body)
	}
}

// Invariant 5: directory answers every enabled person's address, ordered
// by id, to a joined, enabled person with a live session.
func TestKeysEmailDirectory(t *testing.T) {
	w := startKeyWorker(t)
	w.person(adaID, "Ada@Example.com", "owner", "", false)
	w.person(bobID, "Bob@Example.org", "viewer", "", false)
	w.person(carolID, "Carol@Example.com", "member", "", true)
	w.person(daveID, "Dave@Example.com", "member", "", false)
	w.unjoin(daveID)
	viewer := w.session(bobID, time.Hour)

	want := `{"addresses":[` +
		`{"id":"` + adaID + `","address":"Ada@Example.com"},` +
		`{"id":"` + bobID + `","address":"Bob@Example.org"},` +
		`{"id":"` + daveID + `","address":"Dave@Example.com"}]}` + "\n"
	if code, body := w.email(svcsig.AudienceWorkerBrain, emailOp("directory", viewer, nil, "")); code != http.StatusOK || body != want {
		t.Fatalf("directory for a joined viewer = %d %s, want %s", code, body, want)
	}
	for name, hash := range map[string][]byte{
		"an ended session":         w.session(bobID, -time.Second),
		"an unjoined person's":     w.session(daveID, time.Hour),
		"a disabled person's":      w.session(carolID, time.Hour),
		"an unknown hash":          bytes.Repeat([]byte{7}, 32),
		"a link hash, not session": w.link(adaID, time.Hour, false),
	} {
		code, body := w.email(svcsig.AudienceWorkerBrain, emailOp("directory", hash, nil, ""))
		refusedQuietly(t, "directory with "+name, code, body)
	}
}

// Invariant 6 (D7, D9): own, link and member share index-mac's 30 calls
// per 10 s from brain; directory is counted apart.
func TestKeysEmailSharesTheLimit(t *testing.T) {
	w := startKeyWorker(t)
	w.person(adaID, "Ada@Example.com", "owner", "", false)
	w.person(bobID, "Bob@Example.com", "member", "", false)
	ada := w.session(adaID, time.Hour)
	link := w.link(bobID, time.Hour, false)
	mac := `{"address":"Ada@Example.com","all":false}`
	ops := []string{
		emailOp("own", ada, nil, ""),
		emailOp("link", nil, link, ""),
		emailOp("member", ada, nil, bobID),
	}
	for i := range 20 {
		if code, got := w.call(svcsig.AudienceWorkerBrain, http.MethodPost, keys.PathIndexMAC, mac); code != http.StatusOK {
			t.Fatalf("index-mac %d = %d %s", i+1, code, got)
		}
	}
	for i := range 10 {
		if code, got := w.email(svcsig.AudienceWorkerBrain, ops[i%len(ops)]); code != http.StatusOK {
			t.Fatalf("email %d after 20 index-mac calls = %d %s", i+1, code, got)
		}
	}
	for _, op := range ops {
		if code, got := w.email(svcsig.AudienceWorkerBrain, op); code != http.StatusTooManyRequests || got != "keys: refused" {
			t.Fatalf("%s after 30 = %d %q, want 429 keys: refused", op, code, got)
		}
	}
	if code, got := w.call(svcsig.AudienceWorkerBrain, http.MethodPost, keys.PathIndexMAC, mac); code != http.StatusTooManyRequests || got != "keys: refused" {
		t.Fatalf("index-mac 21 = %d %q, want 429 keys: refused", code, got)
	}
	// A body whose op cannot be read takes EmailRate too.
	if code, got := w.email(svcsig.AudienceWorkerBrain, `{"op":`); code != http.StatusTooManyRequests || got != "keys: refused" {
		t.Fatalf("an unreadable body after 30 = %d %q, want 429 keys: refused", code, got)
	}
	// Directory and the 100 key calls are counted apart.
	if code, got := w.email(svcsig.AudienceWorkerBrain, emailOp("directory", ada, nil, "")); code != http.StatusOK {
		t.Fatalf("directory after 30 email calls = %d %s", code, got)
	}
	if code, got := w.call(svcsig.AudienceWorkerBrain, http.MethodPost, keys.PathWrap, `{"kind":"credential","ref":"GITHUB"}`); code != http.StatusOK {
		t.Fatalf("a wrap after 31 email calls = %d %s", code, got)
	}
}

// Invariant 6 (D9): directory takes 300 calls per 10 s from brain, apart
// from index-mac and the other email ops.
func TestKeysEmailDirectoryLimit(t *testing.T) {
	w := startKeyWorker(t)
	w.person(adaID, "Ada@Example.com", "owner", "", false)
	ada := w.session(adaID, time.Hour)
	directory := emailOp("directory", ada, nil, "")
	for i := range 300 {
		if code, got := w.email(svcsig.AudienceWorkerBrain, directory); code != http.StatusOK {
			t.Fatalf("directory %d = %d %s", i+1, code, got)
		}
	}
	if code, got := w.email(svcsig.AudienceWorkerBrain, directory); code != http.StatusTooManyRequests || got != "keys: refused" {
		t.Fatalf("directory 301 = %d %q, want 429 keys: refused", code, got)
	}
	if code, got := w.call(svcsig.AudienceWorkerBrain, http.MethodPost, keys.PathIndexMAC, `{"address":"Ada@Example.com","all":false}`); code != http.StatusOK {
		t.Fatalf("index-mac after 301 directory calls = %d %s", code, got)
	}
	if code, got := w.email(svcsig.AudienceWorkerBrain, emailOp("own", ada, nil, "")); code != http.StatusOK {
		t.Fatalf("own after 301 directory calls = %d %s", code, got)
	}
}

// Invariant 7: every email call writes one keys.email row before it
// answers, with the op and a digest of the proof, and never an address, a
// hash or a user id; a refused audit row refuses the call.
func TestKeysEmailAudit(t *testing.T) {
	w := startKeyWorker(t)
	ctx := context.Background()
	w.person(adaID, "Ada@Example.com", "owner", "", false)
	w.person(bobID, "Bob@Example.com", "member", "", false)
	ada := w.session(adaID, time.Hour)
	link := w.link(bobID, time.Hour, false)
	unknown := bytes.Repeat([]byte{7}, 32)
	for _, call := range []struct {
		audience, body string
		code           int
	}{
		{svcsig.AudienceWorkerBrain, emailOp("own", ada, nil, ""), http.StatusOK},
		{svcsig.AudienceWorkerBrain, emailOp("member", ada, nil, bobID), http.StatusOK},
		{svcsig.AudienceWorkerBrain, emailOp("link", nil, link, ""), http.StatusOK},
		{svcsig.AudienceWorkerBrain, emailOp("directory", ada, nil, ""), http.StatusOK},
		{svcsig.AudienceWorkerBrain, emailOp("own", unknown, nil, ""), http.StatusForbidden},
		{svcsig.AudienceWorkerBrain, emailOp("own", ada, nil, bobID), http.StatusForbidden},
		{svcsig.AudienceWorkerBrain, emailOp("steal", ada, nil, ""), http.StatusForbidden},
		{svcsig.AudienceWorkerGateway, emailOp("own", ada, nil, ""), http.StatusForbidden},
	} {
		if code, _ := w.email(call.audience, call.body); code != call.code {
			t.Fatalf("email %s as %s = %d, want %d", call.body, call.audience, code, call.code)
		}
	}
	digest := func(parts ...[]byte) string { return w.ring.AuditDigest(bytes.Join(parts, nil)) }
	want := []string{
		`{"op": "own", "kind": "email", "audience": "worker-brain", "ref_digest": "` + digest(ada) + `"}`,
		`{"op": "member", "kind": "email", "audience": "worker-brain", "ref_digest": "` + digest(ada, []byte(bobID)) + `"}`,
		`{"op": "link", "kind": "email", "audience": "worker-brain", "ref_digest": "` + digest(link) + `"}`,
		`{"op": "directory", "kind": "email", "audience": "worker-brain", "ref_digest": "` + digest(ada) + `"}`,
		`{"op": "own", "kind": "email", "audience": "worker-brain", "ref_digest": "` + digest(unknown) + `"}`,
		`{"op": "own", "kind": "email", "audience": "worker-brain", "ref_digest": "` + digest(ada, []byte(`"`+bobID+`"`)) + `"}`,
		`{"kind": "email", "audience": "worker-brain"}`,
		`{"kind": "email", "audience": "worker-gateway"}`,
	}
	wantOutcomes := []string{"success", "success", "success", "success", "denied", "denied", "denied", "denied"}
	changes, outcomes := w.auditRows("keys.email")
	if len(changes) != len(want) {
		t.Fatalf("keys.email rows = %v %v", changes, outcomes)
	}
	for i := range want {
		if changes[i] != want[i] || outcomes[i] != wantOutcomes[i] {
			t.Errorf("row %d = %s %s, want %s %s", i, changes[i], outcomes[i], want[i], wantOutcomes[i])
		}
		for _, secret := range []string{"@", "example", adaID, bobID, hex.EncodeToString(ada), base64.StdEncoding.EncodeToString(ada), hex.EncodeToString(link), base64.StdEncoding.EncodeToString(link)} {
			if strings.Contains(strings.ToLower(changes[i]), strings.ToLower(secret)) {
				t.Errorf("row %d holds %q: %s", i, secret, changes[i])
			}
		}
	}

	if _, err := w.owner.Exec(ctx, `ALTER TABLE audit.pending ADD CONSTRAINT test_no_email CHECK (action <> 'keys.email') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	code, body := w.email(svcsig.AudienceWorkerBrain, emailOp("own", ada, nil, ""))
	refusedQuietly(t, "own with the audit refusing", code, body)
}
