package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// SnapshotLesson is one always_inline house rule included in an agent version snapshot.
type SnapshotLesson struct {
	Title        string `json:"title"`
	Cue          string `json:"cue"`
	Body         string `json:"body"`
	AlwaysInline bool   `json:"always_inline"`
}

// Snapshot is the immutable state recorded per agent version.
type Snapshot struct {
	Name                 string            `json:"name"`
	Slug                 string            `json:"slug"`
	Role                 string            `json:"role"`
	Description          string            `json:"description"`
	Instructions         string            `json:"instructions"`
	DefaultRoute         string            `json:"default_route"`
	DefaultModelProvider string            `json:"default_model_provider"`
	DefaultModelID       string            `json:"default_model_id"`
	ToolClasses          []string          `json:"tool_classes"`
	ConnectorIDs         []string          `json:"connector_ids"`
	KnowledgeFolderIDs   []string          `json:"knowledge_folder_ids"`
	PlanApproval         string            `json:"plan_approval"`
	CheckCriteria        []json.RawMessage `json:"check_criteria"`
	Enabled              bool              `json:"enabled"`
	Visibility           string            `json:"visibility"`
	TeamIDs              []string          `json:"team_ids"`
	Installed            bool              `json:"installed"`
	InlineLessons        []SnapshotLesson  `json:"inline_lessons"`
}

// CanonicalSnapshot normalizes slice fields, serializes s into RFC 8785
// canonical JSON, and returns the canonical bytes and 64-char hex SHA-256 digest.
//
// Initial v1 rows created by SQL migration 00001_agents.sql use Postgres
// encode(sha256(snapshot::text::bytea), 'hex'), whereas all Go-created
// snapshots use CanonicalSnapshot; future digest verifiers must handle legacy
// v1 SQL-migrated digests accordingly.
func CanonicalSnapshot(s Snapshot) ([]byte, string, error) {
	if s.ToolClasses == nil {
		s.ToolClasses = []string{}
	}
	if s.ConnectorIDs == nil {
		s.ConnectorIDs = []string{}
	}
	if s.KnowledgeFolderIDs == nil {
		s.KnowledgeFolderIDs = []string{}
	}
	if s.CheckCriteria == nil {
		s.CheckCriteria = []json.RawMessage{}
	}
	if s.TeamIDs == nil {
		s.TeamIDs = []string{}
	}
	if s.InlineLessons == nil {
		s.InlineLessons = []SnapshotLesson{}
	} else if len(s.InlineLessons) > 1 {
		sorted := make([]SnapshotLesson, len(s.InlineLessons))
		copy(sorted, s.InlineLessons)
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].Title < sorted[j].Title
		})
		s.InlineLessons = sorted
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, "", err
	}
	canon, err := CanonicalJSON(raw)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(canon)
	return canon, hex.EncodeToString(sum[:]), nil
}
