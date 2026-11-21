//go:build integration

package postgres_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	chatgateway "github.com/upmo-ai/upmo/internal/chat/adapters/gateway"
	"github.com/upmo-ai/upmo/internal/gateway"
	httpapi "github.com/upmo-ai/upmo/internal/gateway/adapters/http"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	openaiadapter "github.com/upmo-ai/upmo/internal/gateway/adapters/openai"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	knowledgegateway "github.com/upmo-ai/upmo/internal/knowledge/adapters/gateway"
	memorygateway "github.com/upmo-ai/upmo/internal/memory/adapters/gateway"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

const (
	pinAuthor = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa"
	pinOther  = "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"

	pinMine   = "018f3b2c-7a10-7c4a-8e11-000000000001" // the author's account on the route
	pinMine2  = "018f3b2c-7a10-7c4a-8e11-000000000002" // the author's account, on no route
	pinTheirs = "018f3b2c-7a10-7c4a-8e11-000000000003" // the other person's account
	pinHouse  = "018f3b2c-7a10-7c4a-8e11-000000000004" // a house account
	pinGone   = "018f3b2c-7a10-7c4a-8e11-0000000000ff" // an account that does not exist
)

// pinKeys maps each credential ref to the key it resolves to, so a call shows
// whose key it spent.
var pinKeys = map[string]string{
	"pin_mine":   "key-mine",
	"pin_mine2":  "key-mine2",
	"pin_theirs": "key-theirs",
	"pin_house":  "key-house",
}

type pinSecrets map[string]string

func (s pinSecrets) Get(ref string) (string, error) {
	if key, ok := s[ref]; ok {
		return key, nil
	}
	return "", errors.New("missing")
}

// pinCall is one provider call: the model asked for and the key spent.
type pinCall struct{ Model, Key string }

// pinAdapter answers every model except the ones in fail, which fail with the
// class given. It records each call.
type pinAdapter struct {
	calls            []pinCall
	fail             map[string]domain.FailureClass
	streamBeforeFail map[string]string
	usages           []domain.Usage
	delay            time.Duration
}

func (a *pinAdapter) popUsage(fallback domain.Usage) domain.Usage {
	if len(a.usages) == 0 {
		return fallback
	}
	u := a.usages[0]
	a.usages = a.usages[1:]
	return u
}

func (a *pinAdapter) Chat(_ context.Context, attempt app.Attempt, _ domain.Request) (domain.ChatResult, domain.FailureClass, error) {
	if a.delay > 0 {
		time.Sleep(a.delay)
	}
	a.calls = append(a.calls, pinCall{Model: attempt.Model, Key: attempt.Secret})
	if class, bad := a.fail[attempt.Model]; bad {
		return domain.ChatResult{Usage: a.popUsage(domain.Usage{})}, class, fmt.Errorf("pin test: %s", class)
	}
	return domain.ChatResult{Text: "from " + attempt.Model, Usage: a.popUsage(domain.Usage{})}, domain.ClassOK, nil
}

func (a *pinAdapter) Embed(_ context.Context, attempt app.Attempt, inputs []string) ([][]float32, domain.Usage, domain.FailureClass, error) {
	if a.delay > 0 {
		time.Sleep(a.delay)
	}
	a.calls = append(a.calls, pinCall{Model: attempt.Model, Key: attempt.Secret})
	if class, bad := a.fail[attempt.Model]; bad {
		return nil, domain.Usage{}, class, fmt.Errorf("pin test: %s", class)
	}
	vectors := make([][]float32, len(inputs))
	for i := range vectors {
		vectors[i] = make([]float32, domain.EmbedDim)
		vectors[i][0] = 1
	}
	return vectors, domain.Usage{Arrived: true, Input: 1}, domain.ClassOK, nil
}

func (a *pinAdapter) ChatStream(_ context.Context, attempt app.Attempt, _ domain.Request, emit func(string) error) (domain.Usage, domain.FailureClass, error) {
	if a.delay > 0 {
		time.Sleep(a.delay)
	}
	a.calls = append(a.calls, pinCall{Model: attempt.Model, Key: attempt.Secret})
	if class, bad := a.fail[attempt.Model]; bad {
		if text, ok := a.streamBeforeFail[attempt.Model]; ok {
			if err := emit(text); err != nil {
				return domain.Usage{}, domain.ClassCancelled, err
			}
		}
		return a.popUsage(domain.Usage{}), class, fmt.Errorf("pin test: %s", class)
	}
	if err := emit("from " + attempt.Model); err != nil {
		return domain.Usage{}, domain.ClassCancelled, err
	}
	return a.popUsage(domain.Usage{Arrived: true, Input: 1, Output: 1}), domain.ClassOK, nil
}

func (a *pinAdapter) models() string {
	var out []string
	for _, c := range a.calls {
		out = append(out, c.Model)
	}
	return strings.Join(out, ",")
}

func (a *pinAdapter) keysSpent() string {
	var out []string
	for _, c := range a.calls {
		out = append(out, c.Key)
	}
	return strings.Join(out, ",")
}

type pinSink struct {
	text    strings.Builder
	notices []app.StreamNotice
	done    *app.StreamDone
	fail    *app.StreamFail
}

func (s *pinSink) Delta(text string) error { s.text.WriteString(text); return nil }
func (s *pinSink) Notice(n app.StreamNotice) error {
	s.notices = append(s.notices, n)
	return nil
}
func (s *pinSink) Done(d app.StreamDone) { s.done = &d }
func (s *pinSink) Fail(f app.StreamFail) { s.fail = &f }

// pinWorld is a real gateway database with two people, their accounts, a
// house account and three routes, and a dispatch over it whose only adapter
// is the recording one.
type pinWorld struct {
	pool    *pgxpool.Pool
	disp    app.Dispatch
	adapter *pinAdapter
}

func seedPins(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, a := range []struct{ id, owner, name, ref string }{
		{pinMine, pinAuthor, "pin-mine", "pin_mine"},
		{pinMine2, pinAuthor, "pin-mine2", "pin_mine2"},
		{pinTheirs, pinOther, "pin-theirs", "pin_theirs"},
		{pinHouse, "", "pin-house", "pin_house"},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO gateway.provider_accounts
				(id, owner_user_id, provider, name, adapter, credential_ref, plan_kind, house, base_url, visibility)
			VALUES ($1::uuid, NULLIF($2, '')::uuid, 'fake', $3, 'fake', $4, 'metered', $2 = '', '',
				CASE WHEN $2 = '' THEN 'everyone' ELSE 'private' END)`,
			a.id, a.owner, a.name, a.ref); err != nil {
			t.Fatal(err)
		}
	}
	// pin-mixed is the author's own link first, then a house link: the author
	// is served by their own link alone, anyone else by the house one.
	// pin-house is two house links. Every link has no retry budget.
	for _, r := range []struct {
		name       string
		capability string
		links      [][3]string // account, model, position
	}{
		{"pin-mixed", "chat", [][3]string{{pinMine, "m-mine", "1"}, {pinHouse, "m-house", "2"}}},
		{"pin-house", "chat", [][3]string{{pinHouse, "m1", "1"}, {pinHouse, "m2", "2"}}},
		{"pin-vision", "chat", [][3]string{{pinHouse, "m-vis", "1"}, {pinHouse, "m1", "2"}}},
		{"pin-others", "chat", [][3]string{{pinTheirs, "m-theirs", "1"}}},
		{"pin-ledger-embed", "embed", [][3]string{{pinHouse, "m1", "1"}}},
	} {
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO gateway.routes (name, capability, installed) VALUES ($1, $2, true) RETURNING id::text`, r.name, r.capability).Scan(&id); err != nil {
			t.Fatal(err)
		}
		for _, l := range r.links {
			if _, err := pool.Exec(ctx, `
				INSERT INTO gateway.route_links (route_id, position, provider_account_id, model, retry_budget, required_capabilities)
				VALUES ($1::uuid, $4::int, $2::uuid, $3, 0, CASE WHEN $3 = 'm-vis' THEN '{vision}'::text[] ELSE '{}'::text[] END)`, id, l[0], l[1], l[2]); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// pinListed is what each seeded account lists: the models the pin tests ask
// of it.
var pinListed = map[string][]string{
	pinMine:   {"m-mine"},
	pinMine2:  {"x"},
	pinTheirs: {"m1"},
	pinHouse:  {"m1", "m2", "m-vis", "h-pinned", "x"},
}

func newPinWorld(t *testing.T, fail map[string]domain.FailureClass) pinWorld {
	t.Helper()
	pool := pgtest.New(t)
	if err := migrate.Up(context.Background(), pool, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	seedPins(t, pool)
	for account, models := range pinListed {
		for i, model := range models {
			if _, err := pool.Exec(context.Background(), `
				INSERT INTO gateway.provider_models (account_id, model, position) VALUES ($1::uuid, $2, $3)`,
				account, model, i+1); err != nil {
				t.Fatal(err)
			}
		}
	}
	store := postgres.Store{Pool: pool}
	adapter := &pinAdapter{fail: fail}
	return pinWorld{
		pool:    pool,
		adapter: adapter,
		disp: app.Dispatch{
			Routes:  store,
			Pins:    store,
			Ledger:  store,
			Limits:  store,
			Secrets: pinSecrets(pinKeys),
			Caps:    func(string) ([]string, bool) { return []string{"tools"}, true },
			Chat:    map[string]app.Chatter{domain.AdapterFake: adapter},
			Stream:  map[string]app.StreamChatter{domain.AdapterFake: adapter},
			Embed:   map[string]app.Embedder{domain.AdapterFake: adapter},
		},
	}
}

func (w pinWorld) stream(t *testing.T, route, user string, pin domain.Pin) (*pinSink, error) {
	t.Helper()
	sink := &pinSink{}
	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}
	err := w.disp.ChatStreamPinned(context.Background(), route, "", domain.Principal{User: user, Source: "system"}, in, pin, 0, sink)
	return sink, err
}

func TestPinIsTriedFirstAndTheRouteChainIsTheFallback(t *testing.T) {
	// The house account's m2 is on the route; it fails before text, so the
	// chain answers. The notice names the pinned model and the next one.
	w := newPinWorld(t, map[string]domain.FailureClass{"m2": domain.ClassRateLimit})
	sink, err := w.stream(t, "pin-house", pinAuthor, domain.Pin{AccountID: pinHouse, Model: "m2"})
	if err != nil {
		t.Fatal(err)
	}
	if got := w.adapter.models(); got != "m2,m1" {
		t.Fatalf("models called = %s, want the pin first then the chain", got)
	}
	want := []app.StreamNotice{{FailedModel: "m2", Class: domain.ClassRateLimit, NextModel: "m1"}}
	if len(sink.notices) != 1 || sink.notices[0] != want[0] {
		t.Fatalf("notices = %+v, want %+v", sink.notices, want)
	}
	if sink.done == nil || sink.done.Model != "m1" || sink.text.String() != "from m1" {
		t.Fatalf("done = %+v text = %q", sink.done, sink.text.String())
	}
}

func TestPinThatAnswersLeavesTheChainUntouched(t *testing.T) {
	w := newPinWorld(t, nil)
	sink, err := w.stream(t, "pin-house", pinAuthor, domain.Pin{AccountID: pinHouse, Model: "m2"})
	if err != nil {
		t.Fatal(err)
	}
	if w.adapter.models() != "m2" || len(sink.notices) != 0 || sink.done == nil || sink.done.Model != "m2" {
		t.Fatalf("models = %s notices = %+v done = %+v", w.adapter.models(), sink.notices, sink.done)
	}
}

func TestNoPinLeavesTheRouteAsItWas(t *testing.T) {
	w := newPinWorld(t, nil)
	sink, err := w.stream(t, "pin-house", pinAuthor, domain.Pin{})
	if err != nil {
		t.Fatal(err)
	}
	if w.adapter.models() != "m1" || len(sink.notices) != 0 || sink.done == nil || sink.done.Model != "m1" {
		t.Fatalf("models = %s notices = %+v done = %+v", w.adapter.models(), sink.notices, sink.done)
	}
}

func TestPinOnTheRouteIsNotTriedTwice(t *testing.T) {
	// The author's own m-mine is on the route with no retry budget. Pinned,
	// it is tried once, and the chain, which is only their own links, has
	// nothing left: the walk ends with the pin's failure.
	w := newPinWorld(t, map[string]domain.FailureClass{"m-mine": domain.ClassTransient})
	sink, err := w.stream(t, "pin-mixed", pinAuthor, domain.Pin{AccountID: pinMine, Model: "m-mine"})
	if err != nil {
		t.Fatal(err)
	}
	if got := w.adapter.models(); got != "m-mine" {
		t.Fatalf("models called = %s, want m-mine once", got)
	}
	want := app.StreamNotice{FailedModel: "m-mine", Class: domain.ClassTransient, NextModel: ""}
	if len(sink.notices) != 1 || sink.notices[0] != want || sink.fail == nil || sink.fail.Model != "m-mine" {
		t.Fatalf("notices = %+v fail = %+v", sink.notices, sink.fail)
	}
}

func TestPinOffTheRouteGetsTheDefaultRetryBudget(t *testing.T) {
	w := newPinWorld(t, map[string]domain.FailureClass{"x": domain.ClassTransient})
	sink, err := w.stream(t, "pin-house", pinAuthor, domain.Pin{AccountID: pinHouse, Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got := w.adapter.models(); got != "x,x,m1" {
		t.Fatalf("models called = %s, want the pin retried once then the chain", got)
	}
	want := []app.StreamNotice{
		{FailedModel: "x", Class: domain.ClassTransient, NextModel: "x"},
		{FailedModel: "x", Class: domain.ClassTransient, NextModel: "m1"},
	}
	if len(sink.notices) != 2 || sink.notices[0] != want[0] || sink.notices[1] != want[1] {
		t.Fatalf("notices = %+v, want %+v", sink.notices, want)
	}
	if sink.done == nil || sink.done.Model != "m1" {
		t.Fatalf("done = %+v", sink.done)
	}
}

func TestOwnPinOnAHouseOnlyRouteHasNoFallbackAndNeverSpendsTheHouseKey(t *testing.T) {
	// The author has no link on pin-house, which holds house links only. Their
	// own pin is tried and retried and its failure ends the reply, naming the
	// pinned model; the house key is not spent.
	w := newPinWorld(t, map[string]domain.FailureClass{"x": domain.ClassTransient})
	sink, err := w.stream(t, "pin-house", pinAuthor, domain.Pin{AccountID: pinMine2, Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got := w.adapter.keysSpent(); got != "key-mine2,key-mine2" || w.adapter.models() != "x,x" {
		t.Fatalf("models = %s keys = %s, want only the pin twice on the author's key", w.adapter.models(), got)
	}
	if sink.done != nil || sink.fail == nil || sink.fail.Model != "x" {
		t.Fatalf("done = %+v fail = %+v", sink.done, sink.fail)
	}
	want := []app.StreamNotice{
		{FailedModel: "x", Class: domain.ClassTransient, NextModel: "x"},
		{FailedModel: "x", Class: domain.ClassTransient, NextModel: ""},
	}
	if len(sink.notices) != 2 || sink.notices[0] != want[0] || sink.notices[1] != want[1] {
		t.Fatalf("notices = %+v, want %+v", sink.notices, want)
	}
}

func TestPinWorksOnARouteWithNoLinkUsableByThePerson(t *testing.T) {
	// pin-others holds only the other person's link. Both a house pin and an
	// own pin are tried on their own key; the other person's key never is.
	for name, pin := range map[string]domain.Pin{
		"house": {AccountID: pinHouse, Model: "h-pinned"},
		"own":   {AccountID: pinMine2, Model: "x"},
	} {
		w := newPinWorld(t, nil)
		sink, err := w.stream(t, "pin-others", pinAuthor, pin)
		if err != nil {
			t.Fatalf("%s pin: %v", name, err)
		}
		if sink.done == nil || sink.done.Model != pin.Model || w.adapter.models() != pin.Model || strings.Contains(w.adapter.keysSpent(), "key-theirs") {
			t.Fatalf("%s pin: models = %s keys = %s done = %+v", name, w.adapter.models(), w.adapter.keysSpent(), sink.done)
		}
	}
	// Without a pin the same route still has nothing for this person.
	w := newPinWorld(t, nil)
	if _, err := w.stream(t, "pin-others", pinAuthor, domain.Pin{}); err == nil {
		t.Fatal("a route with no link for the person answered without a pin")
	}
}

func TestForeignPinIsRefusedBeforeTheRouteIsLoaded(t *testing.T) {
	// A route that does not exist and a route that is not a chat route would
	// each fail the call; the pin refusal comes first and their key is unused.
	w := newPinWorld(t, nil)
	if _, err := w.pool.Exec(context.Background(), `INSERT INTO gateway.routes (name, capability, installed) VALUES ('pin-embed', 'embed', true)`); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"no-such-route", "pin-embed"} {
		_, err := w.stream(t, route, pinAuthor, domain.Pin{AccountID: pinTheirs, Model: "m1"})
		if !errors.Is(err, domain.ErrPinAccount) {
			t.Fatalf("route %q: err = %v, want the pin refusal", route, err)
		}
	}
	if len(w.adapter.calls) != 0 {
		t.Fatalf("calls = %+v", w.adapter.calls)
	}
}

func TestPinToAnotherPersonsAccountIsRefusedAndTheirKeyIsNeverUsed(t *testing.T) {
	w := newPinWorld(t, nil)
	sink, err := w.stream(t, "pin-house", pinAuthor, domain.Pin{AccountID: pinTheirs, Model: "m1"})
	if !errors.Is(err, domain.ErrPinAccount) {
		t.Fatalf("err = %v, want the refusal", err)
	}
	if len(w.adapter.calls) != 0 || sink.done != nil || sink.fail != nil || len(sink.notices) != 0 || sink.text.Len() != 0 {
		t.Fatalf("calls = %+v sink = %+v", w.adapter.calls, sink)
	}
	for _, id := range []string{pinTheirs, pinOther, "key-theirs", "pin_theirs"} {
		if strings.Contains(err.Error(), id) {
			t.Fatalf("the refusal leaks %q: %v", id, err)
		}
	}
}

func TestOwnPinSpendsOwnKeyAndNeverFallsToTheHouseKey(t *testing.T) {
	// The author pins their second account; it fails, retries once, and
	// fails. They have their own link on the route, so the chain is that
	// link alone, which fails too: the house key is never spent.
	w := newPinWorld(t, map[string]domain.FailureClass{"x": domain.ClassTransient, "m-mine": domain.ClassRateLimit})
	sink, err := w.stream(t, "pin-mixed", pinAuthor, domain.Pin{AccountID: pinMine2, Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got := w.adapter.models(); got != "x,x,m-mine" {
		t.Fatalf("models called = %s", got)
	}
	if got := w.adapter.keysSpent(); got != "key-mine2,key-mine2,key-mine" {
		t.Fatalf("keys spent = %s, want only the author's own", got)
	}
	if sink.done != nil || sink.fail == nil || sink.fail.Model != "m-mine" {
		t.Fatalf("done = %+v fail = %+v", sink.done, sink.fail)
	}
}

func TestHousePinWorksForAnyone(t *testing.T) {
	for _, user := range []string{pinAuthor, pinOther} {
		w := newPinWorld(t, nil)
		sink, err := w.stream(t, "pin-mixed", user, domain.Pin{AccountID: pinHouse, Model: "h-pinned"})
		if err != nil {
			t.Fatal(err)
		}
		if w.adapter.models() != "h-pinned" || w.adapter.keysSpent() != "key-house" || sink.done == nil {
			t.Fatalf("user %s: models = %s keys = %s done = %+v", user, w.adapter.models(), w.adapter.keysSpent(), sink.done)
		}
	}
}

func TestPinThatCannotBeUsedIsSkippedWithANoticeAndTheChainAnswers(t *testing.T) {
	w := newPinWorld(t, nil)
	sink, err := w.stream(t, "pin-house", pinAuthor, domain.Pin{AccountID: pinGone, Model: "ghost"})
	if err != nil {
		t.Fatal(err)
	}
	want := app.StreamNotice{FailedModel: "ghost", Class: domain.ClassModelAccess, NextModel: "m1"}
	if len(sink.notices) != 1 || sink.notices[0] != want {
		t.Fatalf("notices = %+v, want %+v", sink.notices, want)
	}
	if w.adapter.models() != "m1" || sink.done == nil || sink.done.Model != "m1" {
		t.Fatalf("models = %s done = %+v", w.adapter.models(), sink.done)
	}
}

func TestPinOnARouteLinkThatNeedsACapabilityIsSkippedLikeAnyLink(t *testing.T) {
	// m-vis needs vision on this route and the catalog says it has none, so
	// the pin takes the link's need along and is skipped; the chain answers.
	w := newPinWorld(t, nil)
	sink, err := w.stream(t, "pin-vision", pinAuthor, domain.Pin{AccountID: pinHouse, Model: "m-vis"})
	if err != nil {
		t.Fatal(err)
	}
	if w.adapter.models() != "m1" || sink.done == nil || sink.done.Model != "m1" {
		t.Fatalf("models = %s done = %+v", w.adapter.models(), sink.done)
	}
	want := app.StreamNotice{FailedModel: "m-vis", Class: domain.ClassModelAccess, NextModel: "m1"}
	if len(sink.notices) != 1 || sink.notices[0] != want {
		t.Fatalf("notices = %+v, want %+v", sink.notices, want)
	}
}

func TestPinOnCoolingAccountIsSkippedWithANotice(t *testing.T) {
	w := newPinWorld(t, nil)
	gates := app.NewGates()
	gates.Observe("pin_house", domain.ClassRateLimit, time.Hour)
	w.disp.Gates = gates
	sink, err := w.stream(t, "pin-house", pinAuthor, domain.Pin{AccountID: pinHouse, Model: "m2"})
	if err != nil {
		t.Fatal(err)
	}
	want := app.StreamNotice{FailedModel: "m2", Class: domain.ClassModelAccess, NextModel: "m1"}
	if len(sink.notices) < 1 || sink.notices[0] != want || w.adapter.models() != "" {
		t.Fatalf("notices = %+v, want the pinned model first: %+v", sink.notices, want)
	}
}

func TestPinnedAttemptIsLedgerPositionZeroAndRouteLinksKeepTheirs(t *testing.T) {
	w := newPinWorld(t, map[string]domain.FailureClass{"m2": domain.ClassRateLimit})
	if _, err := w.stream(t, "pin-house", pinAuthor, domain.Pin{AccountID: pinHouse, Model: "m2"}); err != nil {
		t.Fatal(err)
	}
	rows, err := w.pool.Query(context.Background(), `SELECT model, link_position FROM gateway.ledger`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var model string
		var pos int
		if err := rows.Scan(&model, &pos); err != nil {
			t.Fatal(err)
		}
		got[model] = pos
	}
	// m2 is route position 2 and was pinned; m1 is route position 1.
	if len(got) != 2 || got["m2"] != 0 || got["m1"] != 1 {
		t.Fatalf("ledger positions = %v, want the pinned m2 at 0 and m1 at 1", got)
	}
}

func TestPinWithoutAPersonOrWithABringYourOwnKeyIsRefused(t *testing.T) {
	w := newPinWorld(t, nil)
	pin := domain.Pin{AccountID: pinHouse, Model: "m1"}
	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}
	if err := w.disp.ChatStreamPinned(context.Background(), "pin-house", "", domain.Principal{}, in, pin, 0, &pinSink{}); !errors.Is(err, domain.ErrPinCaller) {
		t.Fatalf("no person: err = %v", err)
	}
	if err := w.disp.ChatStreamPinned(context.Background(), "pin-house", "pin_mine", domain.Principal{User: pinAuthor}, in, pin, 0, &pinSink{}); !errors.Is(err, domain.ErrPinCaller) {
		t.Fatalf("bring-your-own key: err = %v", err)
	}
	if len(w.adapter.calls) != 0 {
		t.Fatalf("calls = %+v", w.adapter.calls)
	}
}

func TestMalformedPinIsRefusedBeforeAnyCall(t *testing.T) {
	w := newPinWorld(t, nil)
	for _, pin := range []domain.Pin{{AccountID: pinHouse}, {Model: "m1"}, {AccountID: "not-a-uuid", Model: "m1"}} {
		if _, err := w.stream(t, "pin-house", pinAuthor, pin); err == nil {
			t.Fatalf("pin %+v was accepted", pin)
		}
	}
	if len(w.adapter.calls) != 0 {
		t.Fatalf("calls = %+v", w.adapter.calls)
	}
}

// post sends one ChatStream body through the gateway's HTTP handler and
// returns the status and the NDJSON lines.
func (w pinWorld) post(t *testing.T, body string) (int, []string) {
	t.Helper()
	mux := http.NewServeMux()
	httpapi.Handler{Dispatch: w.disp}.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	res, err := http.Post(srv.URL+httpapi.PathChatStream, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var lines []string
	scan := bufio.NewScanner(res.Body)
	for scan.Scan() {
		lines = append(lines, scan.Text())
	}
	return res.StatusCode, lines
}

func seedFakeVendorRate(t *testing.T, w *pinWorld, rateUSDPerMTok float64) {
	t.Helper()
	if _, err := w.pool.Exec(context.Background(), `
		UPDATE gateway.provider_accounts
		SET provider = 'openai', adapter = $2, base_url = 'https://api.openai.com'
		WHERE id = $1::uuid`, pinHouse, domain.AdapterOpenAI); err != nil {
		t.Fatal(err)
	}
	w.disp.Chat[domain.AdapterOpenAI] = w.adapter
	w.disp.Stream[domain.AdapterOpenAI] = w.adapter
	w.disp.Embed[domain.AdapterOpenAI] = w.adapter
	store := postgres.Store{Pool: w.pool}
	if err := store.ReplaceCatalog(context.Background(), "v1", []domain.CatalogRow{{
		Vendor:  "openai",
		Model:   "priced-other",
		Version: "v1",
		Price:   domain.Price{InputPerMTok: &rateUSDPerMTok, OutputPerMTok: &rateUSDPerMTok},
	}}); err != nil {
		t.Fatal(err)
	}
	w.disp.Prices = store
}

func TestFeature_UPM353_EachSourceWritesItsSourceAndPayer(t *testing.T) {
	w := newPinWorld(t, nil)
	seedFakeVendorRate(t, &w, 75.0)
	mux := http.NewServeMux()
	httpapi.Handler{Dispatch: w.disp}.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	serviceKey := "upmo_svc_" + base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))

	chat, err := chatgateway.Open(srv.URL, serviceKey)
	if err != nil {
		t.Fatal(err)
	}
	chatID := "018f3b2c-7a10-7c4a-8e11-cccccccccccc"
	if _, err := chat.ChatStream(context.Background(), chatgateway.Request{
		Route: "pin-house", ChatID: chatID, UserID: pinAuthor, Prompt: "chat",
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	memory, err := memorygateway.Open(srv.URL, serviceKey, "pin-house", "pin-ledger-embed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memory.For(pinAuthor).Propose(context.Background(), "memory"); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.ForTask(pinAuthor).Propose(context.Background(), "task"); err != nil {
		t.Fatal(err)
	}
	knowledge, err := knowledgegateway.Open(srv.URL, serviceKey, "pin-ledger-embed")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := knowledge.For(pinAuthor).Embed(context.Background(), []string{"knowledge"}); err != nil {
		t.Fatal(err)
	}
	call := func(path, body string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	if status := call(httpapi.PathChat, `{"route":"pin-house","prompt":"system","source":"system"}`); status != http.StatusOK {
		t.Fatalf("system = %d", status)
	}
	adapterCalls := len(w.adapter.calls)
	for _, test := range []struct{ path, body string }{
		{httpapi.PathChat, `{"route":"pin-house","prompt":"missing source","user_id":"` + pinAuthor + `"}`},
		{httpapi.PathChat, `{"route":"pin-house","prompt":"missing payer","source":"task"}`},
		{httpapi.PathChat, `{"route":"pin-house","prompt":"bad user","user_id":"not-a-uuid","source":"chat"}`},
		{httpapi.PathChatStream, `{"route":"pin-house","prompt":"bad source","user_id":"` + pinAuthor + `","source":"chat","source_id":"not-a-uuid"}`},
		{httpapi.PathDecide, `{"route":"pin-house","kind":"yesno","prompt":"bad automation","user_id":"` + pinAuthor + `","source":"automation","automation_id":"not-a-uuid"}`},
		{httpapi.PathEmbed, `{"route":"pin-ledger-embed","inputs":["bad"],"user_id":"` + pinAuthor + `","source":"knowledge","source_id":"not-a-uuid"}`},
	} {
		if status := call(test.path, test.body); status != http.StatusBadRequest {
			t.Fatalf("%s = %d, want %d", test.body, status, http.StatusBadRequest)
		}
	}
	if len(w.adapter.calls) != adapterCalls {
		t.Fatalf("invalid requests reached adapter: %d -> %d", adapterCalls, len(w.adapter.calls))
	}

	rows, err := w.pool.Query(context.Background(), `
		SELECT source, COALESCE(user_id::text, ''), COALESCE(source_id::text, ''),
			estimated, COALESCE(price_source, ''), latency_ms
		FROM gateway.ledger
		ORDER BY source`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := map[string]struct{ payer, sourceID string }{
		domain.SourceChat:      {pinAuthor, chatID},
		domain.SourceTask:      {pinAuthor, ""},
		domain.SourceKnowledge: {pinAuthor, ""},
		domain.SourceMemory:    {pinAuthor, ""},
		domain.SourceSystem:    {"", ""},
	}
	got := map[string]struct{ payer, sourceID string }{}
	var estimated bool
	var priceSource string
	var latencyMS *int
	for rows.Next() {
		var source, userID, sourceID string
		if err := rows.Scan(&source, &userID, &sourceID, &estimated, &priceSource, &latencyMS); err != nil {
			t.Fatal(err)
		}
		got[source] = struct{ payer, sourceID string }{userID, sourceID}
		if !estimated || priceSource != "estimate" {
			t.Fatalf("%s estimate = %t %q", source, estimated, priceSource)
		}
		if latencyMS == nil || *latencyMS < 0 {
			t.Fatalf("%s latency = %v", source, latencyMS)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("ledger rows = %v", got)
	}
	for source, attribution := range want {
		if got[source] != attribution {
			t.Errorf("%s attribution = %+v, want %+v", source, got[source], attribution)
		}
	}
}

type endpointRoundTrip func(*http.Request) (*http.Response, error)

func (f endpointRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMantleEmbeddingSkipsWithoutLedgerOrCooldown(t *testing.T) {
	w := newPinWorld(t, nil)
	ctx := context.Background()
	const openAIAccount = "018f3b2c-7a10-7c4a-8e11-000000000005"
	if _, err := w.pool.Exec(ctx, `
		UPDATE gateway.provider_accounts
		SET provider = 'bedrock-mantle', adapter = $2,
		    base_url = 'https://bedrock-mantle.us-east-1.api.aws/v1'
		WHERE id = $1::uuid`, pinHouse, domain.AdapterOpenAI); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pool.Exec(ctx, `
		INSERT INTO gateway.provider_accounts
			(id, owner_user_id, provider, name, adapter, credential_ref, plan_kind, house, base_url, visibility)
		VALUES ($1::uuid, NULL, 'openai', 'pin-openai', $2, 'pin_openai', 'metered', true,
			'https://api.openai.com', 'everyone')`, openAIAccount, domain.AdapterOpenAI); err != nil {
		t.Fatal(err)
	}
	insertRoute := func(name, capability string, accounts ...string) {
		t.Helper()
		var id string
		if err := w.pool.QueryRow(ctx, `INSERT INTO gateway.routes (name, capability, installed) VALUES ($1, $2, true) RETURNING id::text`, name, capability).Scan(&id); err != nil {
			t.Fatal(err)
		}
		for i, account := range accounts {
			if _, err := w.pool.Exec(ctx, `
				INSERT INTO gateway.route_links (route_id, position, provider_account_id, model, retry_budget)
				VALUES ($1::uuid, $2, $3::uuid, $4, 0)`, id, i+1, account, []string{"mantle-model", "openai-embed"}[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
	insertRoute("mantle-fallback", "embed", pinHouse, openAIAccount)
	insertRoute("mantle-only", "embed", pinHouse)
	insertRoute("mantle-chat", "chat", pinHouse)

	var mantleHits, openAIEmbedHits, openAIChatHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/embeddings"):
			openAIEmbedHits.Add(1)
			vector := make([]float32, domain.EmbedDim)
			vector[0] = 1
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"index": 0, "embedding": vector}},
				"usage": map[string]int{"prompt_tokens": 1},
			})
		default:
			openAIChatHits.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "from mantle"}}},
				"usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 1},
			})
		}
	}))
	t.Cleanup(upstream.Close)
	transport := endpointRoundTrip(func(req *http.Request) (*http.Response, error) {
		if strings.HasPrefix(req.URL.Hostname(), "bedrock-mantle.") {
			mantleHits.Add(1)
		}
		moved := req.Clone(req.Context())
		moved.URL.Scheme = "http"
		moved.URL.Host = strings.TrimPrefix(upstream.URL, "http://")
		moved.Host = ""
		return http.DefaultTransport.RoundTrip(moved)
	})
	adapter := openaiadapter.Adapter{Client: &http.Client{Transport: transport}}
	w.disp.Secrets = pinSecrets{"pin_house": "mantle-key", "pin_openai": "openai-key"}
	w.disp.Gates = app.NewGates()
	w.disp.Chat[domain.AdapterOpenAI] = adapter
	w.disp.Stream[domain.AdapterOpenAI] = adapter
	w.disp.Embed[domain.AdapterOpenAI] = adapter

	model, vectors, err := w.disp.EmbedCall(ctx, "mantle-fallback", "", domain.Principal{}, []string{"query"})
	if err != nil || model != "openai-embed" || len(vectors) != 1 || len(vectors[0]) != domain.EmbedDim {
		t.Fatalf("model=%q vectors=%d err=%v", model, len(vectors), err)
	}
	if mantleHits.Load() != 0 || openAIEmbedHits.Load() != 1 {
		t.Fatalf("Mantle hits=%d OpenAI embed hits=%d", mantleHits.Load(), openAIEmbedHits.Load())
	}
	var mantleLedger int
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM gateway.ledger WHERE provider_account_id = $1::uuid`, pinHouse).Scan(&mantleLedger); err != nil {
		t.Fatal(err)
	}
	if mantleLedger != 0 || !w.disp.Gates.Closed("pin_house") {
		t.Fatalf("Mantle ledger rows=%d key available=%t", mantleLedger, w.disp.Gates.Closed("pin_house"))
	}

	if _, err := w.disp.ChatCall(ctx, "mantle-chat", "", domain.Principal{}, domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hello"}}}); err != nil {
		t.Fatalf("Mantle chat after embed skip: %v", err)
	}
	if mantleHits.Load() != 1 || openAIChatHits.Load() != 1 || !w.disp.Gates.Closed("pin_house") {
		t.Fatalf("after Mantle chat: Mantle hits=%d OpenAI chat hits=%d key available=%t", mantleHits.Load(), openAIChatHits.Load(), w.disp.Gates.Closed("pin_house"))
	}

	mux := http.NewServeMux()
	httpapi.Handler{Dispatch: w.disp}.Mount(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, httpapi.PathEmbed, strings.NewReader(`{"route":"mantle-only","inputs":["query"]}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("Mantle-only embedding status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
}

func TestFeature_UPM353_SeededTotalsMatchUSDPricing(t *testing.T) {
	w := newPinWorld(t, nil)
	if _, err := w.pool.Exec(context.Background(), `
		UPDATE gateway.provider_accounts
		SET provider = 'openai', adapter = $2, base_url = 'https://api.openai.com'
		WHERE id = $1::uuid`, pinHouse, domain.AdapterOpenAI); err != nil {
		t.Fatal(err)
	}
	w.disp.Chat[domain.AdapterOpenAI] = w.adapter
	w.disp.Stream[domain.AdapterOpenAI] = w.adapter
	w.disp.Embed[domain.AdapterOpenAI] = w.adapter
	store := postgres.Store{Pool: w.pool}
	inRate, outRate, cacheReadRate, cacheWriteRate := 1.5, 3.7, 0.31, 2.13
	if err := store.ReplaceCatalog(context.Background(), "v1", []domain.CatalogRow{{
		Vendor:  "openai",
		Model:   "m1",
		Version: "v1",
		Price: domain.Price{
			InputPerMTok:   &inRate,
			OutputPerMTok:  &outRate,
			CacheReadMTok:  &cacheReadRate,
			CacheWriteMTok: &cacheWriteRate,
		},
	}}); err != nil {
		t.Fatal(err)
	}
	w.disp.Prices = store
	usages := []domain.Usage{
		{Arrived: true, Input: 1},
		{Arrived: true, Input: 2, Output: 1, CacheRead: 1, CacheWrite: 1},
		{Arrived: true, Input: 7, Output: 3, CacheRead: 5, CacheWrite: 2},
		{Arrived: true, Input: 11, Output: 5, CacheRead: 13, CacheWrite: 7},
	}
	w.adapter.usages = append([]domain.Usage(nil), usages...)
	for range usages {
		if _, err := w.stream(t, "pin-house", pinAuthor, domain.Pin{}); err != nil {
			t.Fatal(err)
		}
	}
	var got int64
	if err := w.pool.QueryRow(context.Background(), `SELECT SUM(cost_micro_usd) FROM gateway.ledger`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	var want int64
	for _, u := range usages {
		usd := (float64(u.Input)*inRate + float64(u.Output)*outRate + float64(u.CacheRead)*cacheReadRate + float64(u.CacheWrite)*cacheWriteRate) / 1_000_000
		want += int64(math.Round(usd * 1_000_000))
	}
	if got != want {
		t.Fatalf("ledger micro-USD = %d, hand-priced USD gives %d", got, want)
	}
}

func TestFeature_UPM353_UnpricedCallUsesProviderCatalogEstimate(t *testing.T) {
	w := newPinWorld(t, nil)
	seedFakeVendorRate(t, &w, 10.0)
	if _, err := w.disp.ChatCall(context.Background(), "pin-house", "", domain.Principal{User: pinAuthor, Source: domain.SourceChat}, domain.Input{
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "12345678"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.disp.ChatCall(context.Background(), "pin-house", "", domain.Principal{User: pinAuthor, Source: domain.SourceChat}, domain.Input{
		Tools: []domain.Tool{{Name: "lookup", Description: "find docs", Schema: json.RawMessage(`{"type":"object"}`)}},
		Messages: []domain.Message{
			{Role: domain.RoleUser, Text: "12345678"},
			{Role: domain.RoleAssistant, ToolCalls: []domain.ToolCall{{ID: "tc-1", Name: "lookup", Arguments: json.RawMessage(`{"query":"abcdefgh12345678"}`)}}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := w.pool.Query(context.Background(), `SELECT cost_micro_usd, estimated, price_source FROM gateway.ledger ORDER BY at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var costs []int64
	for rows.Next() {
		var cost int64
		var estimated bool
		var source string
		if err := rows.Scan(&cost, &estimated, &source); err != nil {
			t.Fatal(err)
		}
		if !estimated || source != "estimate" {
			t.Fatalf("estimate = %d %t %q", cost, estimated, source)
		}
		costs = append(costs, cost)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(costs) != 2 || costs[0] != 20 || costs[1] != 820 {
		t.Fatalf("estimate costs = %v, want [20, 820]", costs)
	}
}

func TestFeature_UPM353_FailedAttemptIsKnownZero(t *testing.T) {
	w := newPinWorld(t, nil)
	w.disp.Secrets = pinSecrets{}
	_, err := w.disp.ChatCall(context.Background(), "pin-house", "", domain.Principal{User: pinAuthor, Source: domain.SourceChat}, domain.Input{
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hello"}},
	})
	if err == nil {
		t.Fatal("credential failure succeeded")
	}
	var cost int64
	var estimated, charged bool
	var source string
	var latencyMS *int
	if err := w.pool.QueryRow(context.Background(), `SELECT cost_micro_usd, estimated, charged_to_limit, price_source, latency_ms FROM gateway.ledger`).Scan(&cost, &estimated, &charged, &source, &latencyMS); err != nil {
		t.Fatal(err)
	}
	if cost != 0 || estimated || charged || source != "not_billed" || latencyMS != nil {
		t.Fatalf("failed attempt = %d %t %t %q latency=%v", cost, estimated, charged, source, latencyMS)
	}
}

func TestFeature_UPM353_FailedAttemptWithOutputIsEstimated(t *testing.T) {
	w := newPinWorld(t, map[string]domain.FailureClass{"m1": domain.ClassCancelled})
	seedFakeVendorRate(t, &w, 75.0)
	w.adapter.streamBeforeFail = map[string]string{"m1": "partial "}
	sink := &pinSink{}
	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hello"}}}
	if err := w.disp.ChatStreamPinned(context.Background(), "pin-house", "", domain.Principal{User: pinAuthor, Source: domain.SourceChat}, in, domain.Pin{}, 0, sink); err != nil {
		t.Fatal(err)
	}
	if sink.text.String() != "partial " || sink.done != nil || sink.fail == nil || sink.fail.Class != domain.ClassCancelled {
		t.Fatalf("sink text=%q done=%+v fail=%+v", sink.text.String(), sink.done, sink.fail)
	}
	rows, err := w.pool.Query(context.Background(), `SELECT cost_micro_usd, estimated, price_source FROM gateway.ledger`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type row struct {
		cost      int64
		estimated bool
		source    string
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.cost, &r.estimated, &r.source); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// 5 input bytes ("hello") + 8 streamed bytes ("partial ") = 13 bytes ->
	// ceil(13/4) = 4 tokens at EstimatedMicroUSDPerMTok (75 micro-USD/token) = 300;
	// without the streamed bytes ceil(5/4) = 2 tokens = 150.
	if len(got) != 1 || got[0].cost != 300 || !got[0].estimated || got[0].source != "estimate" {
		t.Fatalf("failed attempt with output = %+v, want cost=300", got)
	}
}

func TestFeature_UPM353_ChargedLimitAndLatencyRules(t *testing.T) {
	w := newPinWorld(t, nil)
	ctx := context.Background()
	for _, person := range []struct{ id, email, role string }{
		{pinOther, "other@example.com", "owner"},
		{pinAuthor, "author@example.com", "member"},
	} {
		if _, err := w.pool.Exec(ctx, `
			INSERT INTO identity.users (id, email, name, role_id, joined_at)
			VALUES ($1::uuid, $2, $2, (SELECT id FROM identity.roles WHERE key = $3), now())`,
			person.id, person.email, person.role); err != nil {
			t.Fatal(err)
		}
	}
	var teamID string
	if err := w.pool.QueryRow(ctx, `INSERT INTO identity.teams (name) VALUES ('Engineering') RETURNING id::text`).Scan(&teamID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pool.Exec(ctx, `INSERT INTO identity.team_members (team_id, user_id) VALUES ($1::uuid, $2::uuid)`, teamID, pinAuthor); err != nil {
		t.Fatal(err)
	}
	const (
		pinTeam     = "018f3b2c-7a10-7c4a-8e11-000000000011"
		pinEveryone = "018f3b2c-7a10-7c4a-8e11-000000000012"
		pinSub      = "018f3b2c-7a10-7c4a-8e11-000000000013"
	)
	for _, acc := range []struct {
		id, owner, name, ref, plan, vis, model string
		house                                  bool
		teams                                  []string
	}{
		{pinTeam, pinOther, "pin-team", "pin_team", domain.PlanMetered, domain.VisibilityTeam, "m-team", false, []string{teamID}},
		{pinEveryone, pinOther, "pin-everyone", "pin_everyone", domain.PlanMetered, domain.VisibilityEveryone, "m-everyone", false, []string{}},
		{pinSub, "", "pin-sub", "pin_sub", domain.PlanSubscription, domain.VisibilityEveryone, "m-sub", true, []string{}},
	} {
		if _, err := w.pool.Exec(ctx, `
			INSERT INTO gateway.provider_accounts
				(id, owner_user_id, provider, name, adapter, credential_ref, plan_kind, house, base_url, visibility, team_ids)
			VALUES ($1::uuid, NULLIF($2, '')::uuid, 'fake', $3, 'fake', $4, $5, $6, '', $7, $8::uuid[])`,
			acc.id, acc.owner, acc.name, acc.ref, acc.plan, acc.house, acc.vis, acc.teams); err != nil {
			t.Fatal(err)
		}
		if _, err := w.pool.Exec(ctx, `
			INSERT INTO gateway.provider_models (account_id, model, position)
			VALUES ($1::uuid, $2, 1)`, acc.id, acc.model); err != nil {
			t.Fatal(err)
		}
	}
	secrets := pinSecrets{}
	for k, v := range pinKeys {
		secrets[k] = v
	}
	secrets["pin_team"] = "key-team"
	secrets["pin_everyone"] = "key-everyone"
	secrets["pin_sub"] = "key-sub"
	w.disp.Secrets = secrets
	w.disp.Teams = postgres.Store{Pool: w.pool}
	w.adapter.delay = 30 * time.Millisecond

	cases := []struct {
		name      string
		accountID string
		model     string
		charged   bool
	}{
		{"house", pinHouse, "m1", true},
		{"team", pinTeam, "m-team", true},
		{"everyone", pinEveryone, "m-everyone", true},
		{"private", pinMine, "m-mine", false},
		{"subscription", pinSub, "m-sub", false},
	}
	for _, tc := range cases {
		sink, err := w.stream(t, "pin-house", pinAuthor, domain.Pin{AccountID: tc.accountID, Model: tc.model})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(sink.notices) != 0 || sink.done == nil || sink.done.Model != tc.model {
			t.Fatalf("%s: notices=%+v done=%+v", tc.name, sink.notices, sink.done)
		}
	}

	rows, err := w.pool.Query(ctx, `SELECT model, charged_to_limit, latency_ms FROM gateway.ledger`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type ledgerRow struct {
		charged bool
		latency *int
	}
	got := map[string]ledgerRow{}
	for rows.Next() {
		var model string
		var charged bool
		var latency *int
		if err := rows.Scan(&model, &charged, &latency); err != nil {
			t.Fatal(err)
		}
		got[model] = ledgerRow{charged: charged, latency: latency}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(cases) {
		t.Fatalf("ledger rows = %v, want %d", got, len(cases))
	}
	for _, tc := range cases {
		row, ok := got[tc.model]
		if !ok {
			t.Fatalf("missing ledger row for %s (%s)", tc.name, tc.model)
		}
		if row.charged != tc.charged {
			t.Errorf("%s (%s) charged_to_limit = %t, want %t", tc.name, tc.model, row.charged, tc.charged)
		}
		if row.latency == nil || *row.latency < 30 {
			t.Errorf("%s (%s) latency_ms = %v, want >= 30", tc.name, tc.model, row.latency)
		}
	}
}

func TestChatStreamOverHTTPCarriesThePin(t *testing.T) {
	w := newPinWorld(t, map[string]domain.FailureClass{"m2": domain.ClassRateLimit})
	status, lines := w.post(t, fmt.Sprintf(
		`{"route":"pin-house","user_id":%q,"source":"chat","prompt":"hi","pin":{"account_id":%q,"model":"m2"}}`, pinAuthor, pinHouse))
	if status != http.StatusOK || w.adapter.models() != "m2,m1" {
		t.Fatalf("status = %d models = %s lines = %v", status, w.adapter.models(), lines)
	}
	if len(lines) < 2 || !strings.Contains(lines[0], `"failed_model":"m2"`) || !strings.Contains(lines[0], `"next_model":"m1"`) {
		t.Fatalf("lines = %v", lines)
	}

	status, lines = w.post(t, fmt.Sprintf(
		`{"route":"pin-house","user_id":%q,"source":"chat","prompt":"hi","pin":{"account_id":%q,"model":"m1"}}`, pinAuthor, pinTheirs))
	if status != http.StatusBadRequest || len(lines) != 1 || strings.Contains(lines[0], pinTheirs) {
		t.Fatalf("foreign pin: status = %d lines = %v", status, lines)
	}
}

// listModels gives accounts the models they list, as a person adds them in
// the provider settings.
func (w pinWorld) listModels(t *testing.T, account string, models ...string) {
	t.Helper()
	for i, model := range models {
		if _, err := w.pool.Exec(context.Background(), `
			INSERT INTO gateway.provider_models (account_id, model, position)
			VALUES ($1::uuid, $2, (SELECT COALESCE(MAX(position), 0) + 1 FROM gateway.provider_models WHERE account_id = $1::uuid))
			ON CONFLICT DO NOTHING`,
			account, model); err != nil {
			t.Fatalf("model %d: %v", i, err)
		}
	}
}

func TestProviderPinResolvesToTheCallersOwnAccountBeforeTheHouseOne(t *testing.T) {
	// The author's second account and the house account both list m2. The
	// author's own is used, on their key; the other person's account, which
	// lists it too, never is. With an own pin there is no house fallback.
	w := newPinWorld(t, map[string]domain.FailureClass{"m2": domain.ClassTransient})
	w.listModels(t, pinMine2, "m2")
	w.listModels(t, pinHouse, "m2")
	w.listModels(t, pinTheirs, "m2")
	sink, err := w.stream(t, "pin-house", pinAuthor, domain.Pin{Provider: "FAKE", Model: "m2"})
	if err != nil {
		t.Fatal(err)
	}
	if got := w.adapter.keysSpent(); got != "key-mine2,key-mine2" || w.adapter.models() != "m2,m2" {
		t.Fatalf("models = %s keys = %s, want the author's own key twice and nothing else", w.adapter.models(), got)
	}
	if sink.done != nil || sink.fail == nil || sink.fail.Model != "m2" {
		t.Fatalf("done = %+v fail = %+v", sink.done, sink.fail)
	}
}

func TestProviderPinFallsToTheHouseAccountWhenTheCallerHasNoneThatListsTheModel(t *testing.T) {
	w := newPinWorld(t, nil)
	w.listModels(t, pinMine, "other-model") // theirs, but not this model
	w.listModels(t, pinHouse, "h-pinned")
	for _, user := range []string{pinAuthor, pinOther} {
		w.adapter.calls = nil
		sink, err := w.stream(t, "pin-house", user, domain.Pin{Provider: "fake", Model: "h-pinned"})
		if err != nil {
			t.Fatal(err)
		}
		if w.adapter.models() != "h-pinned" || w.adapter.keysSpent() != "key-house" || sink.done == nil || len(sink.notices) != 0 {
			t.Fatalf("user %s: models = %s keys = %s done = %+v notices = %+v", user, w.adapter.models(), w.adapter.keysSpent(), sink.done, sink.notices)
		}
	}
}

func TestProviderPinWithNoUsableAccountIsSkippedWithANoticeNamingTheModel(t *testing.T) {
	w := newPinWorld(t, nil)
	w.listModels(t, pinTheirs, "only-theirs")
	for name, pin := range map[string]domain.Pin{
		"nobody lists the model":     {Provider: "fake", Model: "unlisted"},
		"only another person does":   {Provider: "fake", Model: "only-theirs"},
		"no account of the provider": {Provider: "nope", Model: "m1"},
	} {
		w.adapter.calls = nil
		sink, err := w.stream(t, "pin-house", pinAuthor, pin)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := app.StreamNotice{FailedModel: pin.Model, Class: domain.ClassModelAccess, NextModel: "m1"}
		if len(sink.notices) != 1 || sink.notices[0] != want {
			t.Fatalf("%s: notices = %+v, want %+v", name, sink.notices, want)
		}
		if w.adapter.models() != "m1" || w.adapter.keysSpent() != "key-house" || sink.done == nil || sink.done.Model != "m1" {
			t.Fatalf("%s: models = %s keys = %s done = %+v, want the chain to answer", name, w.adapter.models(), w.adapter.keysSpent(), sink.done)
		}
	}
}

func TestChatStreamOverHTTPCarriesAProviderPinAndRefusesAnAmbiguousOne(t *testing.T) {
	w := newPinWorld(t, nil)
	w.listModels(t, pinHouse, "h-pinned")
	status, lines := w.post(t, fmt.Sprintf(
		`{"route":"pin-house","user_id":%q,"source":"chat","prompt":"hi","pin":{"provider":"fake","model":"h-pinned"}}`, pinAuthor))
	if status != http.StatusOK || w.adapter.models() != "h-pinned" || len(lines) < 2 {
		t.Fatalf("status = %d models = %s lines = %v", status, w.adapter.models(), lines)
	}
	w.adapter.calls = nil
	status, lines = w.post(t, fmt.Sprintf(
		`{"route":"pin-house","user_id":%q,"source":"chat","prompt":"hi","pin":{"account_id":%q,"provider":"fake","model":"h-pinned"}}`, pinAuthor, pinHouse))
	if status != http.StatusBadRequest || len(w.adapter.calls) != 0 {
		t.Fatalf("both account and provider: status = %d calls = %+v lines = %v", status, w.adapter.calls, lines)
	}
}

func TestAccountPinNeedsTheAccountToListTheModel(t *testing.T) {
	// pinHouse lists m2 but not m-unlisted: the first pin answers, the second
	// is skipped with a notice naming it and the route chain answers.
	w := newPinWorld(t, nil)
	sink, err := w.stream(t, "pin-house", pinAuthor, domain.Pin{AccountID: pinHouse, Model: "m2"})
	if err != nil {
		t.Fatal(err)
	}
	if w.adapter.models() != "m2" || sink.done == nil || sink.done.Model != "m2" || len(sink.notices) != 0 {
		t.Fatalf("listed: models = %s done = %+v notices = %+v", w.adapter.models(), sink.done, sink.notices)
	}

	w = newPinWorld(t, nil)
	sink, err = w.stream(t, "pin-house", pinAuthor, domain.Pin{AccountID: pinHouse, Model: "m-unlisted"})
	if err != nil {
		t.Fatal(err)
	}
	want := app.StreamNotice{FailedModel: "m-unlisted", Class: domain.ClassModelAccess, NextModel: "m1"}
	if len(sink.notices) != 1 || sink.notices[0] != want {
		t.Fatalf("notices = %+v, want %+v", sink.notices, want)
	}
	if w.adapter.models() != "m1" || sink.done == nil || sink.done.Model != "m1" {
		t.Fatalf("unlisted: models = %s done = %+v", w.adapter.models(), sink.done)
	}
}
