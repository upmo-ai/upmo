package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	// ParentCanceled is the cause stored when a parent cancel fails a child.
	ParentCanceled = "parent_canceled"
	// CauseCanceled is a question the person refused.
	CauseCanceled = "canceled"
	// CauseAccess is a task that lost the access it needed.
	CauseAccess = "access"
	// CauseStalled is a verification loop that repeated the same gap.
	CauseStalled = "stalled"
	// CauseFailing is a verification loop that kept missing.
	CauseFailing = "failing"
	// CauseReviewLimit is a review that used its rounds.
	CauseReviewLimit = "review_limit"
	// CauseUnknown is a check whose outcome could not be read.
	CauseUnknown = "unknown"
	// ResultSummaryRunes is the longest result summary.
	ResultSummaryRunes = 200
	// ResultDetailRunes is the longest result detail.
	ResultDetailRunes = 32000
	// WorktreeRunes is the longest worktree name.
	WorktreeRunes = 80
)

var (
	// ErrResult is a result that is empty, too long, or already stored.
	ErrResult = errors.New("tasks: result")
	// ErrWorktree is a worktree name that is empty, or already held by an open task.
	ErrWorktree = errors.New("tasks: worktree")
)

// Result is the fixed shape stored for a child.
type Result struct {
	Summary string `json:"summary"`
	Detail  string `json:"detail"`
}

// AcceptResult trims a result and checks its lengths. The caller neutralises
// the text first.
func AcceptResult(summary, detail string) (Result, error) {
	summary = strings.TrimSpace(summary)
	detail = strings.TrimSpace(detail)
	if summary == "" || utf8.RuneCountInString(summary) > ResultSummaryRunes {
		return Result{}, ErrResult
	}
	if utf8.RuneCountInString(detail) > ResultDetailRunes {
		return Result{}, ErrResult
	}
	return Result{Summary: summary, Detail: detail}, nil
}

// Worktree trims a worktree name. It allows letters, digits, and . _ - /.
func Worktree(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > WorktreeRunes {
		return "", ErrWorktree
	}
	for _, r := range name {
		if !worktreeRune(r) {
			return "", ErrWorktree
		}
	}
	return name, nil
}

func worktreeRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= 'A' && r <= 'Z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '.' || r == '_' || r == '-' || r == '/':
		return true
	default:
		return false
	}
}
