package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	// DailyCap is the most proposals a read-only scout may raise in one day.
	DailyCap = 5
	// ProposalRunes is the longest proposal title.
	ProposalRunes = 200
	// NoticeProposal is the notice kind a scout raises.
	NoticeProposal = "proposal"
)

// ErrScout is a scout call the domain refuses: a negative count, an
// over-long title, or a title that looks like it carries a credential.
var ErrScout = errors.New("tasks: scout")

// scoutMarkers are lower-case fragments that mark a title as carrying a
// credential. The match ignores case.
var scoutMarkers = []string{"upmo_pat_", "upmo_s2s_", "upmo_svc_", "bearer", "sk-"}

// Notice is one proposal a scout raises. It names no task and opens nothing.
type Notice struct {
	Kind  string
	Title string
}

// Propose turns scout titles into proposal notices. already is the number of
// proposals raised today. Each title is trimmed; a blank or a repeat is
// dropped. One over-long title, or one that looks like a credential, refuses
// the whole list. At most DailyCap-already notices are returned. Propose never
// returns a task id, opens a task or fires an automation.
func Propose(already int, titles []string) ([]Notice, error) {
	if already < 0 {
		return nil, ErrScout
	}
	if already >= DailyCap {
		return []Notice{}, nil
	}
	room := DailyCap - already
	notices := make([]Notice, 0, room)
	kept := make(map[string]struct{}, len(titles))
	for _, title := range titles {
		title = strings.TrimSpace(title)
		if title == "" {
			continue
		}
		if utf8.RuneCountInString(title) > ProposalRunes || scoutSecret(title) {
			return nil, ErrScout
		}
		if _, seen := kept[title]; seen {
			continue
		}
		kept[title] = struct{}{}
		if len(notices) < room {
			notices = append(notices, Notice{Kind: NoticeProposal, Title: title})
		}
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
