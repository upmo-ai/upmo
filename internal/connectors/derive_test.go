package connectors

import (
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/identity"
)

// The identity facade does not re-export the verdict constants, so the test
// spells them as the facade's ConnectorVerdict type.
const (
	verdictAllow identity.ConnectorVerdict = "allow"
	verdictAsk   identity.ConnectorVerdict = "ask"
)

func TestDeriveVerbs(t *testing.T) {
	cases := []struct {
		kind, verb string
		wantVerb   string
		action     identity.ConnectorAction
		verdict    identity.ConnectorVerdict
	}{
		{KindOpenAPI, "get", "GET", identity.ConnectorRead, verdictAllow},
		{KindOpenAPI, "HEAD", "HEAD", identity.ConnectorRead, verdictAllow},
		{KindOpenAPI, "OPTIONS", "OPTIONS", identity.ConnectorRead, verdictAllow},
		{KindDiscovery, "GET", "GET", identity.ConnectorRead, verdictAllow},
		{KindOpenAPI, "POST", "POST", identity.ConnectorChange, verdictAsk},
		{KindOpenAPI, "PUT", "PUT", identity.ConnectorChange, verdictAsk},
		{KindOpenAPI, "PATCH", "PATCH", identity.ConnectorChange, verdictAsk},
		{KindOpenAPI, "DELETE", "DELETE", identity.ConnectorDelete, verdictAsk},
		{KindGraphQL, "query", "query", identity.ConnectorRead, verdictAllow},
		{KindGraphQL, "Query", "query", identity.ConnectorRead, verdictAllow},
		{KindGraphQL, "mutation", "mutation", identity.ConnectorChange, verdictAsk},
	}
	for _, c := range cases {
		t.Run(c.kind+"/"+c.verb, func(t *testing.T) {
			tool, err := Derive(Op{Kind: c.kind, Name: "listUsers", Verb: c.verb, Ref: "github"})
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if tool.Verb != c.wantVerb || tool.Action != c.action {
				t.Fatalf("tool = %+v", tool)
			}
			got := identity.ConnectorDecide(identity.DefaultConnectorGrant("github"), tool.Action, "")
			if got != c.verdict {
				t.Fatalf("decide = %s, want %s", got, c.verdict)
			}
		})
	}
}

func TestDeriveRejects(t *testing.T) {
	ok := Op{Kind: KindOpenAPI, Name: "listUsers", Verb: "GET", Ref: "github"}
	with := func(f func(*Op)) Op {
		op := ok
		f(&op)
		return op
	}
	cases := []struct {
		label string
		op    Op
	}{
		{"graphql subscription", with(func(o *Op) { o.Kind, o.Verb = KindGraphQL, "subscription" })},
		{"graphql GET", with(func(o *Op) { o.Kind, o.Verb = KindGraphQL, "GET" })},
		{"openapi query", with(func(o *Op) { o.Verb = "query" })},
		{"openapi TRACE", with(func(o *Op) { o.Verb = "TRACE" })},
		{"openapi blank verb", with(func(o *Op) { o.Verb = "" })},
		{"kind google", with(func(o *Op) { o.Kind = "google" })},
		{"kind blank", with(func(o *Op) { o.Kind = "" })},
		{"name blank", with(func(o *Op) { o.Name = "" })},
		{"name dotdot", with(func(o *Op) { o.Name = "../etc" })},
		{"name slash", with(func(o *Op) { o.Name = "list/users" })},
		{"ref blank", with(func(o *Op) { o.Ref = "" })},
		{"ref upper", with(func(o *Op) { o.Ref = "GitHub" })},
		{"ref url", with(func(o *Op) { o.Ref = "https://example.com" })},
		{"ref bearer", with(func(o *Op) { o.Ref = "Bearer secret" })},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			tool, err := Derive(c.op)
			if !errors.Is(err, ErrSpec) {
				t.Fatalf("err = %v", err)
			}
			if tool != (Tool{}) {
				t.Fatalf("tool = %+v", tool)
			}
		})
	}
}

func TestDeriveAccepts(t *testing.T) {
	cases := []struct {
		name, ref string
	}{
		{"listUsers", "github"},
		{"listUsers", "github/install"},
	}
	for _, c := range cases {
		t.Run(c.ref, func(t *testing.T) {
			tool, err := Derive(Op{Kind: KindOpenAPI, Name: c.name, Verb: "GET", Ref: c.ref})
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if tool.Name != c.name || tool.Ref != c.ref || tool.Kind != KindOpenAPI {
				t.Fatalf("tool = %+v", tool)
			}
		})
	}
}

func TestDeriveAll(t *testing.T) {
	ops := []Op{
		{Kind: KindOpenAPI, Name: "listUsers", Verb: "get", Ref: "github"},
		{Kind: KindOpenAPI, Name: "deleteUser", Verb: "DELETE", Ref: "github"},
	}
	tools, err := DeriveAll(ops)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "listUsers" || tools[1].Name != "deleteUser" {
		t.Fatalf("tools = %+v", tools)
	}
	if tools[0].Verb != "GET" || tools[1].Action != identity.ConnectorDelete {
		t.Fatalf("tools = %+v", tools)
	}
	if ops[0].Verb != "get" {
		t.Fatalf("input mutated: %q", ops[0].Verb)
	}
}

func TestDeriveAllRejects(t *testing.T) {
	cases := []struct {
		label string
		ops   []Op
	}{
		{"different refs", []Op{
			{Kind: KindOpenAPI, Name: "listUsers", Verb: "GET", Ref: "github"},
			{Kind: KindOpenAPI, Name: "listRepos", Verb: "GET", Ref: "gitlab"},
		}},
		{"duplicate name", []Op{
			{Kind: KindOpenAPI, Name: "listUsers", Verb: "GET", Ref: "github"},
			{Kind: KindOpenAPI, Name: "listUsers", Verb: "POST", Ref: "github"},
		}},
		{"empty", []Op{}},
		{"nil", nil},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			tools, err := DeriveAll(c.ops)
			if !errors.Is(err, ErrSpec) {
				t.Fatalf("err = %v", err)
			}
			if tools != nil {
				t.Fatalf("tools = %+v", tools)
			}
		})
	}
}
