package app

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/agents/domain"
)

// LessonFields holds the fields of a house rule.
type LessonFields struct {
	Title        string
	Cue          string
	Body         string
	AlwaysInline bool
}

// LessonChange holds a partial edit to a house rule.
type LessonChange struct {
	Title        *string
	Cue          *string
	Body         *string
	AlwaysInline *bool
}

// LessonBook keeps two-tier house rules for agents under user access control.
type LessonBook interface {
	InlineLessons(ctx context.Context, access Access, agentID string) ([]domain.Lesson, error)
	ListLessonCards(ctx context.Context, access Access, agentID string) ([]domain.Card, error)
	OpenLesson(ctx context.Context, access Access, agentID, lessonID string) (domain.Lesson, error)
	SaveLesson(ctx context.Context, access Access, agentID string, in LessonFields, at time.Time) (string, error)
	UpdateLesson(ctx context.Context, access Access, agentID, lessonID string, in LessonFields, at time.Time) error
	RemoveLesson(ctx context.Context, access Access, agentID, lessonID string, at time.Time) error
}

// AgentReader reads house rules in a transaction scoped to one running agent.
type AgentReader interface {
	Inline(ctx context.Context, agentID string) ([]domain.Lesson, error)
	Index(ctx context.Context, agentID string) ([]domain.Card, error)
	Open(ctx context.Context, agentID, lessonID string) (domain.Lesson, error)
}

// Playbook runs read-only progressive-disclosure transactions scoped to one agent.
type Playbook interface {
	InAgent(ctx context.Context, agentID string, fn func(AgentReader) error) error
}

// InlineLessons returns full always_inline lessons for an agent visible to access.
func InlineLessons(ctx context.Context, book LessonBook, access Access, agentID string) ([]domain.Lesson, error) {
	if err := agentCaller(access); err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(agentID); err != nil {
		return nil, ErrAgentNotFound
	}
	got, err := book.InlineLessons(ctx, access, agentID)
	if err != nil {
		return nil, err
	}
	if got == nil {
		got = []domain.Lesson{}
	}
	return got, nil
}

// Index returns lightweight [Title -> Cue] cards (<= 24, no body) for
// non-inline lessons of an agent visible to access.
func Index(ctx context.Context, book LessonBook, access Access, agentID string) ([]domain.Card, error) {
	if err := agentCaller(access); err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(agentID); err != nil {
		return nil, ErrAgentNotFound
	}
	cards, err := book.ListLessonCards(ctx, access, agentID)
	if err != nil {
		return nil, err
	}
	if cards == nil {
		cards = []domain.Card{}
	}
	return cards, nil
}

// Open returns one full lesson (including body) on demand.
func Open(ctx context.Context, book LessonBook, access Access, agentID, lessonID string) (domain.Lesson, error) {
	if err := agentCaller(access); err != nil {
		return domain.Lesson{}, err
	}
	if _, err := uuid.Parse(agentID); err != nil {
		return domain.Lesson{}, ErrAgentNotFound
	}
	if _, err := uuid.Parse(lessonID); err != nil {
		return domain.Lesson{}, domain.ErrLessonGone
	}
	return book.OpenLesson(ctx, access, agentID, lessonID)
}

// AgentInlineLessons loads always_inline lessons scoped to the running agent.
func AgentInlineLessons(ctx context.Context, pb Playbook, agentID string) ([]domain.Lesson, error) {
	if _, err := uuid.Parse(agentID); err != nil {
		return nil, ErrAgentNotFound
	}
	var out []domain.Lesson
	err := pb.InAgent(ctx, agentID, func(r AgentReader) error {
		var err error
		out, err = r.Inline(ctx, agentID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []domain.Lesson{}
	}
	return out, nil
}

// AgentIndex loads non-inline [Title -> Cue] cards scoped to the running agent.
func AgentIndex(ctx context.Context, pb Playbook, agentID string) ([]domain.Card, error) {
	if _, err := uuid.Parse(agentID); err != nil {
		return nil, ErrAgentNotFound
	}
	var out []domain.Card
	err := pb.InAgent(ctx, agentID, func(r AgentReader) error {
		var err error
		out, err = r.Index(ctx, agentID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []domain.Card{}
	}
	return out, nil
}

// AgentOpen opens one lesson's body scoped to the running agent.
func AgentOpen(ctx context.Context, pb Playbook, agentID, lessonID string) (domain.Lesson, error) {
	if _, err := uuid.Parse(agentID); err != nil {
		return domain.Lesson{}, ErrAgentNotFound
	}
	if _, err := uuid.Parse(lessonID); err != nil {
		return domain.Lesson{}, domain.ErrLessonGone
	}
	var out domain.Lesson
	err := pb.InAgent(ctx, agentID, func(r AgentReader) error {
		var err error
		out, err = r.Open(ctx, agentID, lessonID)
		return err
	})
	return out, err
}

// SaveLesson validates and stores a house rule. If AlwaysInline is true, the
// store increments the agent's version and inserts a new snapshot row.
func SaveLesson(ctx context.Context, book LessonBook, access Access, agentID string, in LessonFields, at time.Time) (string, error) {
	if err := agentCaller(access); err != nil {
		return "", err
	}
	if _, err := uuid.Parse(agentID); err != nil {
		return "", ErrAgentNotFound
	}
	title, cue, body, err := domain.CleanLesson(in.Title, in.Cue, in.Body)
	if err != nil {
		return "", err
	}
	in.Title, in.Cue, in.Body = title, cue, body
	return book.SaveLesson(ctx, access, agentID, in, at)
}

// ChangeLesson applies a partial edit to a house rule. If either before or after
// is always_inline, the store increments the agent's version and stamps a snapshot.
func ChangeLesson(ctx context.Context, book LessonBook, access Access, agentID, lessonID string, ch LessonChange, at time.Time) error {
	if err := agentCaller(access); err != nil {
		return err
	}
	if _, err := uuid.Parse(agentID); err != nil {
		return ErrAgentNotFound
	}
	if _, err := uuid.Parse(lessonID); err != nil {
		return domain.ErrLessonGone
	}
	current, err := book.OpenLesson(ctx, access, agentID, lessonID)
	if err != nil {
		return err
	}
	fields := LessonFields{
		Title:        current.Title,
		Cue:          current.Cue,
		Body:         current.Body,
		AlwaysInline: current.AlwaysInline,
	}
	if ch.Title != nil {
		fields.Title = *ch.Title
	}
	if ch.Cue != nil {
		fields.Cue = *ch.Cue
	}
	if ch.Body != nil {
		fields.Body = *ch.Body
	}
	if ch.AlwaysInline != nil {
		fields.AlwaysInline = *ch.AlwaysInline
	}
	title, cue, body, err := domain.CleanLesson(fields.Title, fields.Cue, fields.Body)
	if err != nil {
		return err
	}
	fields.Title, fields.Cue, fields.Body = title, cue, body
	return book.UpdateLesson(ctx, access, agentID, lessonID, fields, at)
}

// RemoveLesson deletes a house rule. If it was always_inline, the store stamps
// a new agent version snapshot.
func RemoveLesson(ctx context.Context, book LessonBook, access Access, agentID, lessonID string, at time.Time) error {
	if err := agentCaller(access); err != nil {
		return err
	}
	if _, err := uuid.Parse(agentID); err != nil {
		return ErrAgentNotFound
	}
	if _, err := uuid.Parse(lessonID); err != nil {
		return domain.ErrLessonGone
	}
	return book.RemoveLesson(ctx, access, agentID, lessonID, at)
}
