package domain

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

const maxResources = 64
const maxResourceLen = 256

var connectorSlug = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

var (
	// ErrConnector is an unknown connector slug.
	ErrConnector = errors.New("identity: unknown connector")
	// ErrResourceScope is a missing or mixed resource list.
	ErrResourceScope = errors.New("identity: choose all or only these")
	// ErrChanges is an unknown change policy.
	ErrChanges = errors.New("identity: choose allow, ask or block")
)

// ResourceScope is which resources on a connector a run may touch.
type ResourceScope string

const (
	ScopeAll   ResourceScope = "all"
	ScopeThese ResourceScope = "these"
)

// ChangePolicy is what a run may do to a resource.
type ChangePolicy string

const (
	ChangesAllow ChangePolicy = "allow"
	ChangesAsk   ChangePolicy = "ask"
	ChangesBlock ChangePolicy = "block"
)

// ConnectorAction is one use of a connector.
type ConnectorAction string

const (
	ConnectorRead   ConnectorAction = "read"
	ConnectorChange ConnectorAction = "change"
	ConnectorDelete ConnectorAction = "delete"
)

// ConnectorVerdict is allow, ask or block.
type ConnectorVerdict string

const (
	ConnectorAllow ConnectorVerdict = "allow"
	ConnectorAsk   ConnectorVerdict = "ask"
	ConnectorBlock ConnectorVerdict = "block"
)

// ConnectorGrant is one person's rule for one connector.
type ConnectorGrant struct {
	Connector string
	Scope     ResourceScope
	Resources []string
	Changes   ChangePolicy
}

// DefaultConnectorGrant is All plus Ask first. A missing row means this.
func DefaultConnectorGrant(connector string) ConnectorGrant {
	return ConnectorGrant{Connector: connector, Scope: ScopeAll, Changes: ChangesAsk}
}

// ParseConnector accepts a lowercase slug.
func ParseConnector(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !connectorSlug.MatchString(name) {
		return "", ErrConnector
	}
	return name, nil
}

// ParseChanges accepts allow, ask or block. Empty is ask.
func ParseChanges(text string) (ChangePolicy, error) {
	switch strings.TrimSpace(text) {
	case "", string(ChangesAsk):
		return ChangesAsk, nil
	case string(ChangesAllow):
		return ChangesAllow, nil
	case string(ChangesBlock):
		return ChangesBlock, nil
	default:
		return "", ErrChanges
	}
}

// ParseConnectorGrant builds a grant or returns a typed error.
func ParseConnectorGrant(connector, scope string, resources []string, changes string) (ConnectorGrant, error) {
	name, err := ParseConnector(connector)
	if err != nil {
		return ConnectorGrant{}, err
	}
	policy, err := ParseChanges(changes)
	if err != nil {
		return ConnectorGrant{}, err
	}
	switch strings.TrimSpace(scope) {
	case string(ScopeAll), "":
		if len(resources) > 0 {
			return ConnectorGrant{}, ErrResourceScope
		}
		return ConnectorGrant{Connector: name, Scope: ScopeAll, Changes: policy}, nil
	case string(ScopeThese):
		ids, err := parseResourceIDs(resources)
		if err != nil {
			return ConnectorGrant{}, err
		}
		return ConnectorGrant{Connector: name, Scope: ScopeThese, Resources: ids, Changes: policy}, nil
	default:
		return ConnectorGrant{}, ErrResourceScope
	}
}

// ConnectorDecide is the safety rule for one action on one resource.
func ConnectorDecide(grant ConnectorGrant, action ConnectorAction, resource string) ConnectorVerdict {
	if !inScope(grant, resource) {
		return ConnectorBlock
	}
	if action == ConnectorDelete {
		return ConnectorAsk
	}
	if action == ConnectorRead {
		return ConnectorAllow
	}
	switch grant.Changes {
	case ChangesAllow:
		return ConnectorAllow
	case ChangesBlock:
		return ConnectorBlock
	default:
		return ConnectorAsk
	}
}

func inScope(grant ConnectorGrant, resource string) bool {
	if grant.Scope != ScopeThese {
		return true
	}
	if resource == "" {
		return true
	}
	for _, id := range grant.Resources {
		if id == resource {
			return true
		}
	}
	return false
}

func parseResourceIDs(in []string) ([]string, error) {
	if len(in) == 0 || len(in) > maxResources {
		return nil, ErrResourceScope
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		id := strings.TrimSpace(raw)
		if id == "" || utf8.RuneCountInString(id) > maxResourceLen || seen[id] {
			return nil, ErrResourceScope
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}
