package connectors

import (
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/identity"
)

// ErrCatalog reports a catalog that cannot be built and never carries tool text.
var ErrCatalog = errors.New("connectors: catalog")

// Entry is one tool as a sandbox sees it, without a credential reference.
type Entry struct {
	Name   string
	Kind   string
	Verb   string
	Action identity.ConnectorAction
}

// credentialMarks are substrings that mark a credential and refuse the catalog.
var credentialMarks = []string{"upmo_pat_", "upmo_s2s_", "upmo_svc_", "bearer", "sk-"}

// Catalog re-derives each tool for one user and run and drops every credential reference.
func Catalog(user, run string, tools []Tool) ([]Entry, error) {
	u, err := uuid.Parse(strings.TrimSpace(user))
	if err != nil || u == uuid.Nil {
		return nil, ErrCatalog
	}
	r, err := uuid.Parse(strings.TrimSpace(run))
	if err != nil || r == uuid.Nil || r == u {
		return nil, ErrCatalog
	}
	entries := make([]Entry, 0, len(tools))
	names := make(map[string]struct{}, len(tools))
	for _, in := range tools {
		if hasCredentialMark(in.Name, in.Kind, in.Verb, string(in.Action), in.Ref) {
			return nil, ErrCatalog
		}
		tool, err := Derive(Op{Kind: in.Kind, Name: in.Name, Verb: in.Verb, Ref: in.Ref})
		if err != nil {
			return nil, ErrCatalog
		}
		if _, dup := names[tool.Name]; dup {
			return nil, ErrCatalog
		}
		names[tool.Name] = struct{}{}
		entries = append(entries, Entry{Name: tool.Name, Kind: tool.Kind, Verb: tool.Verb, Action: tool.Action})
	}
	return entries, nil
}

// hasCredentialMark reports whether any field contains a credential mark.
func hasCredentialMark(fields ...string) bool {
	for _, f := range fields {
		low := strings.ToLower(f)
		for _, m := range credentialMarks {
			if strings.Contains(low, m) {
				return true
			}
		}
	}
	return false
}
