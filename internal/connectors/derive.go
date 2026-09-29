package connectors

import (
	"errors"
	"strings"

	"github.com/upmo-ai/upmo/internal/identity"
)

// ErrSpec reports an operation that does not derive a tool. It never carries
// the ref or other operation text.
var ErrSpec = errors.New("connectors: spec")

// Spec kinds a tool can come from.
const (
	KindOpenAPI   = "openapi"
	KindGraphQL   = "graphql"
	KindDiscovery = "discovery"
)

// Op is one declared operation in a spec.
type Op struct {
	Kind string
	Name string
	Verb string
	Ref  string
}

// Tool is a derived operation with its connector action.
type Tool struct {
	Kind   string
	Name   string
	Verb   string
	Ref    string
	Action identity.ConnectorAction
}

// Derive turns one operation into a tool. It does not decide or dial.
func Derive(op Op) (Tool, error) {
	kind := strings.ToLower(strings.TrimSpace(op.Kind))
	name := strings.TrimSpace(op.Name)
	ref := strings.TrimSpace(op.Ref)
	if !validName(name) || !validRef(ref) {
		return Tool{}, ErrSpec
	}
	var verb string
	var action identity.ConnectorAction
	switch kind {
	case KindOpenAPI, KindDiscovery:
		verb = strings.ToUpper(strings.TrimSpace(op.Verb))
		switch verb {
		case "GET", "HEAD", "OPTIONS":
			action = identity.ConnectorRead
		case "POST", "PUT", "PATCH":
			action = identity.ConnectorChange
		case "DELETE":
			action = identity.ConnectorDelete
		default:
			return Tool{}, ErrSpec
		}
	case KindGraphQL:
		verb = strings.ToLower(strings.TrimSpace(op.Verb))
		switch verb {
		case "query":
			action = identity.ConnectorRead
		case "mutation":
			action = identity.ConnectorChange
		default:
			return Tool{}, ErrSpec
		}
	default:
		return Tool{}, ErrSpec
	}
	return Tool{Kind: kind, Name: name, Verb: verb, Ref: ref, Action: action}, nil
}

// DeriveAll derives every operation of one spec. All tools share the first
// tool's kind and ref, and names are unique. Any failure returns nil.
func DeriveAll(ops []Op) ([]Tool, error) {
	if len(ops) == 0 {
		return nil, ErrSpec
	}
	tools := make([]Tool, 0, len(ops))
	names := make(map[string]struct{}, len(ops))
	for _, op := range ops {
		tool, err := Derive(op)
		if err != nil {
			return nil, ErrSpec
		}
		if len(tools) > 0 && (tool.Kind != tools[0].Kind || tool.Ref != tools[0].Ref) {
			return nil, ErrSpec
		}
		if _, dup := names[tool.Name]; dup {
			return nil, ErrSpec
		}
		names[tool.Name] = struct{}{}
		tools = append(tools, tool)
	}
	return tools, nil
}

// validName is a letter, then up to 63 letters, digits, dots, underscores, or hyphens.
func validName(s string) bool {
	if len(s) == 0 || len(s) > 64 || !isLetter(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !isLetter(c) && !isDigit(c) && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// validRef is one lowercase slug, or two separated by a single slash.
func validRef(s string) bool {
	parts := strings.Split(s, "/")
	if len(parts) > 2 {
		return false
	}
	for _, p := range parts {
		if !validSlug(p) {
			return false
		}
	}
	return true
}

// validSlug is a lowercase letter, then up to 63 lowercase letters, digits, or hyphens.
func validSlug(s string) bool {
	if len(s) == 0 || len(s) > 64 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && !isDigit(c) && c != '-' {
			return false
		}
	}
	return true
}

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
