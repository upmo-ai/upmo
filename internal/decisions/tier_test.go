package decisions

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const tierUser = "018f3b2c-7a10-7c4a-8e11-111111111111"

var tierHops = []Hop{
	{Position: 1, Adapter: "openai", Model: "small", Weight: 1, Retry: 0},
}

func TestTierRouteRejectsInvalidCanariesBeforeDeciding(t *testing.T) {
	tests := []struct {
		name     string
		canaries []Canary
	}{
		{
			name: "no canaries",
		},
		{
			name: "blank goal",
			canaries: []Canary{{
				Goal:      " \t ",
				Tiers:     []string{"tier_a", "tier_b"},
				Reference: "tier_b",
				Succeeded: true,
			}},
		},
		{
			name: "goal over two hundred runes",
			canaries: []Canary{{
				Goal:      strings.Repeat("é", 201),
				Tiers:     []string{"tier_a", "tier_b"},
				Reference: "tier_b",
				Succeeded: true,
			}},
		},
		{
			name: "one offered tier",
			canaries: []Canary{{
				Goal:      "Route task",
				Tiers:     []string{"tier_b"},
				Reference: "tier_b",
				Succeeded: true,
			}},
		},
		{
			name: "reference not offered",
			canaries: []Canary{{
				Goal:      "Route task",
				Tiers:     []string{"tier_a", "tier_b"},
				Reference: "tier_c",
				Succeeded: true,
			}},
		},
		{
			name: "reference never succeeded",
			canaries: []Canary{{
				Goal:      "Route task",
				Tiers:     []string{"tier_a", "tier_b"},
				Reference: "tier_b",
				Succeeded: false,
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			book := &tierGates{}
			run := &tierRunner{answers: []tierAnswer{{yes: true, choice: "tier_a"}}}
			err := TierRoute(
				context.Background(),
				book,
				tierAccess(),
				tierHops,
				100,
				test.canaries,
				run,
			)
			if !errors.Is(err, ErrConfig) {
				t.Fatalf("error = %v", err)
			}
			if run.calls != 0 {
				t.Fatalf("runner calls = %d", run.calls)
			}
			if len(book.saved) != 0 {
				t.Fatalf("saved = %+v", book.saved)
			}
		})
	}
}

func TestTierRouteRefusesCallerWithoutSettingsManage(t *testing.T) {
	book := &tierGates{}
	run := &tierRunner{answers: []tierAnswer{{yes: true, choice: "tier_a"}}}
	access := Access{UserID: tierUser, Allow: "task:edit"}
	err := TierRoute(
		context.Background(),
		book,
		access,
		tierHops,
		100,
		oneTierCanary(),
		run,
	)
	if !errors.Is(err, ErrAccess) {
		t.Fatalf("error = %v", err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
}

func TestTierRouteSkipsRunnerForStoredFingerprint(t *testing.T) {
	fp, err := Fingerprint(Config{Name: RouteTier, Kind: KindChoice, Hops: tierHops})
	if err != nil {
		t.Fatal(err)
	}
	book := &tierGates{fingerprint: fp, accepted: true}
	run := &tierRunner{answers: []tierAnswer{{yes: true, choice: "tier_a"}}}
	if err := TierRoute(
		context.Background(),
		book,
		tierAccess(),
		tierHops,
		100,
		oneTierCanary(),
		run,
	); err != nil {
		t.Fatal(err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestTierRouteRejectsNilRunnerForNewFingerprint(t *testing.T) {
	book := &tierGates{}
	err := TierRoute(
		context.Background(),
		book,
		tierAccess(),
		tierHops,
		100,
		oneTierCanary(),
		nil,
	)
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestTierRouteStoresPassingParity(t *testing.T) {
	book := &tierGates{}
	run := &tierRunner{answers: []tierAnswer{
		{yes: true, choice: "tier_a"},
		{yes: true, choice: "tier_a"},
		{yes: true, choice: "tier_a"},
		{yes: false, choice: "tier_a"},
	}}
	if err := TierRoute(
		context.Background(),
		book,
		tierAccess(),
		tierHops,
		75,
		fourTierCanaries(),
		run,
	); err != nil {
		t.Fatal(err)
	}
	if len(book.saved) != 1 {
		t.Fatalf("saved = %d", len(book.saved))
	}
	got := book.saved[0]
	if got.Route != RouteTier || got.Kind != KindChoice || got.Hit != 3 || got.Total != 4 || got.Bar != 75 {
		t.Fatalf("gate = %+v", got)
	}
}

func TestTierRouteRejectsParityBelowBar(t *testing.T) {
	book := &tierGates{}
	run := &tierRunner{answers: []tierAnswer{
		{yes: true, choice: "tier_a"},
		{yes: true, choice: "tier_a"},
		{yes: true, choice: "tier_a"},
		{yes: false, choice: "tier_a"},
	}}
	err := TierRoute(
		context.Background(),
		book,
		tierAccess(),
		tierHops,
		100,
		fourTierCanaries(),
		run,
	)
	if !errors.Is(err, ErrGate) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestTierRouteKeepsReferenceOutcomeAndIgnoresReferenceFailure(t *testing.T) {
	book := &tierGates{}
	run := &tierRunner{answers: []tierAnswer{
		{yes: false, choice: "tier_b"},
		{yes: false, choice: "tier_a"},
	}}
	canaries := []Canary{
		{
			Goal:      "Route first task",
			Tiers:     []string{"tier_a", "tier_b"},
			Reference: "tier_b",
			Succeeded: true,
		},
		{
			Goal:      "Route second task",
			Tiers:     []string{"tier_a", "tier_b"},
			Reference: "tier_b",
			Succeeded: false,
		},
	}
	if err := TierRoute(
		context.Background(),
		book,
		tierAccess(),
		tierHops,
		100,
		canaries,
		run,
	); err != nil {
		t.Fatal(err)
	}
	if len(book.saved) != 1 {
		t.Fatalf("saved = %d", len(book.saved))
	}
	got := book.saved[0]
	if got.Hit != 1 || got.Total != 1 || got.Bar != 100 {
		t.Fatalf("gate = %+v", got)
	}
}

func TestTierRouteRejectsChoiceThatWasNotOffered(t *testing.T) {
	book := &tierGates{}
	run := &tierRunner{answers: []tierAnswer{{yes: true, choice: "tier_c"}}}
	canaries := []Canary{
		{
			Goal:      "Route failed reference task",
			Tiers:     []string{"tier_a", "tier_b"},
			Reference: "tier_b",
			Succeeded: false,
		},
		{
			Goal:      "Route passing reference task",
			Tiers:     []string{"tier_a", "tier_b"},
			Reference: "tier_b",
			Succeeded: true,
		},
	}
	err := TierRoute(
		context.Background(),
		book,
		tierAccess(),
		tierHops,
		100,
		canaries,
		run,
	)
	if !errors.Is(err, ErrGate) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestTierRouteReturnsRunnerErrorWithoutSaving(t *testing.T) {
	want := errors.New("runner failed")
	book := &tierGates{}
	run := &tierRunner{answers: []tierAnswer{{err: want}}}
	err := TierRoute(
		context.Background(),
		book,
		tierAccess(),
		tierHops,
		100,
		oneTierCanary(),
		run,
	)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestTierRouteTrimsWithoutMutatingCallerTiers(t *testing.T) {
	book := &tierGates{}
	tiers := []string{" tier_a ", "\ttier_b "}
	canaries := []Canary{{
		Goal:      " Route task ",
		Tiers:     tiers,
		Reference: " tier_b ",
		Succeeded: true,
	}}
	wantTiers := append([]string(nil), tiers...)
	run := &tierRunner{
		answers:       []tierAnswer{{yes: true, choice: " tier_a "}},
		mutateOptions: true,
	}
	if err := TierRoute(
		context.Background(),
		book,
		tierAccess(),
		tierHops,
		100,
		canaries,
		run,
	); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tiers, wantTiers) {
		t.Fatalf("caller tiers = %q", tiers)
	}
	wantOptions := [][]string{{"tier_a", "tier_b"}}
	if !reflect.DeepEqual(run.options, wantOptions) {
		t.Fatalf("runner options = %q", run.options)
	}
}

func oneTierCanary() []Canary {
	return []Canary{{
		Goal:      "Route task",
		Tiers:     []string{"tier_a", "tier_b"},
		Reference: "tier_b",
		Succeeded: true,
	}}
}

func fourTierCanaries() []Canary {
	return []Canary{
		{Goal: "Route task one", Tiers: []string{"tier_a", "tier_b"}, Reference: "tier_b", Succeeded: true},
		{Goal: "Route task two", Tiers: []string{"tier_a", "tier_b"}, Reference: "tier_b", Succeeded: true},
		{Goal: "Route task three", Tiers: []string{"tier_a", "tier_b"}, Reference: "tier_b", Succeeded: true},
		{Goal: "Route task four", Tiers: []string{"tier_a", "tier_b"}, Reference: "tier_b", Succeeded: true},
	}
}

func tierAccess() Access {
	return Access{UserID: tierUser, Allow: "settings:manage"}
}

type tierGates struct {
	fingerprint string
	accepted    bool
	saved       []Gate
}

func (g *tierGates) Accepted(context.Context, Access, string) (string, bool, error) {
	return g.fingerprint, g.accepted, nil
}

func (g *tierGates) Save(_ context.Context, _ Access, gate Gate) error {
	g.saved = append(g.saved, gate)
	return nil
}

type tierAnswer struct {
	yes    bool
	choice string
	err    error
}

type tierRunner struct {
	answers       []tierAnswer
	calls         int
	goals         []string
	options       [][]string
	mutateOptions bool
}

func (r *tierRunner) Decide(_ context.Context, goal string, options []string) (bool, string, error) {
	call := r.calls
	r.calls++
	r.goals = append(r.goals, goal)
	r.options = append(r.options, append([]string(nil), options...))
	if r.mutateOptions {
		options[0] = "tier_b"
	}
	if call < len(r.answers) {
		answer := r.answers[call]
		return answer.yes, answer.choice, answer.err
	}
	return false, "", nil
}
