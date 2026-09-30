package connectors

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/identity"
)

const (
	catalogUser = "6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b"
	catalogRun  = "7a2b3c4d-5e6f-4a71-9b8c-0d1e2f3a4b5c"
)

// catalogTool derives one openapi GET tool for the catalog tests.
func catalogTool(t *testing.T, name string) Tool {
	t.Helper()
	tool, err := Derive(Op{Kind: KindOpenAPI, Name: name, Verb: "GET", Ref: "github"})
	if err != nil {
		t.Fatalf("derive err = %v", err)
	}
	return tool
}

func TestCatalog(t *testing.T) {
	tools := []Tool{catalogTool(t, "listUsers")}
	entries, err := Catalog(catalogUser, catalogRun, tools)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	e := entries[0]
	if e.Name != "listUsers" || e.Kind != KindOpenAPI || e.Verb != "GET" || e.Action != identity.ConnectorRead {
		t.Fatalf("entry = %+v", e)
	}
	if strings.Contains(fmt.Sprintf("%+v", e), "github") {
		t.Fatalf("entry carries the ref: %+v", e)
	}
	if tools[0].Ref != "github" {
		t.Fatalf("input mutated: %q", tools[0].Ref)
	}
}

func TestCatalogEmpty(t *testing.T) {
	entries, err := Catalog(catalogUser, catalogRun, []Tool{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if entries == nil || len(entries) != 0 {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestCatalogRejects(t *testing.T) {
	cases := []struct {
		label     string
		user, run string
		tools     []Tool
	}{
		{"nil user", "", catalogRun, nil},
		{"nil uuid", "00000000-0000-0000-0000-000000000000", catalogRun, nil},
		{"blank run", catalogUser, "  ", nil},
		{"run equals user", catalogUser, catalogUser, nil},
		{"credential name", catalogUser, catalogRun, []Tool{
			{Kind: KindOpenAPI, Name: "sk-live", Verb: "GET", Ref: "github", Action: identity.ConnectorRead},
		}},
		{"credential name upper", catalogUser, catalogRun, []Tool{
			{Kind: KindOpenAPI, Name: "SK-live", Verb: "GET", Ref: "github", Action: identity.ConnectorRead},
		}},
		{"duplicate name", catalogUser, catalogRun, []Tool{
			catalogTool(t, "listUsers"),
			catalogTool(t, "listUsers"),
		}},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			entries, err := Catalog(c.user, c.run, c.tools)
			if !errors.Is(err, ErrCatalog) {
				t.Fatalf("err = %v", err)
			}
			if entries != nil {
				t.Fatalf("entries = %+v", entries)
			}
		})
	}
}
