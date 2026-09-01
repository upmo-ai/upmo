package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// MaxFacts is the most claims one propose call may return.
const MaxFacts = 5

// Proposal is one model-proposed claim. Unknown JSON fields are rejected.
type Proposal struct {
	Claim           string `json:"claim"`
	ChangesBehavior bool   `json:"changes_behavior"`
}

// ParseProposals reads a strict JSON array of proposals. One bad element
// rejects the batch. An empty array is success.
func ParseProposals(raw []byte) ([]Proposal, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('[') {
		return nil, fmt.Errorf("propose: want a JSON array")
	}
	var batch []Proposal
	for dec.More() {
		var obj json.RawMessage
		if err := dec.Decode(&obj); err != nil {
			return nil, fmt.Errorf("propose: %w", err)
		}
		item, err := parseProposal(obj)
		if err != nil {
			return nil, err
		}
		batch = append(batch, item)
		if len(batch) > MaxFacts {
			return nil, fmt.Errorf("propose: more than %d facts", MaxFacts)
		}
	}
	tok, err = dec.Token()
	if err != nil || tok != json.Delim(']') {
		return nil, fmt.Errorf("propose: want a JSON array")
	}
	if dec.More() {
		return nil, fmt.Errorf("propose: trailing data")
	}
	return batch, nil
}

func parseProposal(raw json.RawMessage) (Proposal, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return Proposal{}, fmt.Errorf("propose: %w", err)
	}
	if _, ok := keys["claim"]; !ok {
		return Proposal{}, fmt.Errorf("propose: missing claim")
	}
	if _, ok := keys["changes_behavior"]; !ok {
		return Proposal{}, fmt.Errorf("propose: missing changes_behavior")
	}
	if len(keys) != 2 {
		return Proposal{}, fmt.Errorf("propose: unknown field")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var item Proposal
	if err := dec.Decode(&item); err != nil {
		return Proposal{}, fmt.Errorf("propose: %w", err)
	}
	item.Claim = strings.TrimSpace(item.Claim)
	if item.Claim == "" {
		return Proposal{}, fmt.Errorf("propose: empty claim")
	}
	return item, nil
}
