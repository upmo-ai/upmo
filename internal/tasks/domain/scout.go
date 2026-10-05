package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	// ProposalRunes is the longest proposal title.
	ProposalRunes = 200
	// NoticeProposal is the notice kind a scout raises.
	NoticeProposal = "proposal"
)

// ErrScout is a scout call the domain refuses: an over-long title, or a title
// that looks like it carries a credential.
var ErrScout = errors.New("tasks: scout")

// scoutMarkers are lower-case fragments that mark a title as carrying a
// credential. The match ignores case.
var scoutMarkers = []string{"upmo_pat_", "upmo_s2s_", "upmo_svc_", "bearer", "sk-"}

// Notice is one proposal a scout raises. It names no task and opens nothing.
type Notice struct {
	Kind  string
	Title string
}

// Propose turns scout titles into proposal notices. Each title is trimmed; a
// blank, a repeat, or a match to an open goal is dropped. One over-long title,
// or one that looks like a credential, refuses the whole list. Propose never
// returns a task id, opens a task or fires an automation.
func Propose(titles []string, openGoals []string) ([]Notice, error) {
	open := make(map[string]struct{}, len(openGoals))
	for _, goal := range openGoals {
		norm := strings.ToLower(strings.TrimSpace(goal))
		if norm != "" {
			open[norm] = struct{}{}
		}
	}
	notices := make([]Notice, 0, len(titles))
	kept := make(map[string]struct{}, len(titles))
	for _, title := range titles {
		title = strings.TrimSpace(title)
		if title == "" {
			continue
		}
		if utf8.RuneCountInString(title) > ProposalRunes || scoutSecret(title) {
			return nil, ErrScout
		}
		norm := strings.ToLower(title)
		if _, dup := open[norm]; dup {
			continue
		}
		if _, seen := kept[title]; seen {
			continue
		}
		kept[title] = struct{}{}
		notices = append(notices, Notice{Kind: NoticeProposal, Title: title})
	}
	return notices, nil
}

// scoutSecret reports whether a title contains a credential marker.
func scoutSecret(title string) bool {
	lower := strings.ToLower(title)
	for _, marker := range scoutMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
