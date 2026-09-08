package app

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/memory/domain"
)

// A lesson is not a fact. It belongs to an agent, not a person, so it has no
// user id, no embedding and no trust value. An admin writes it; the agent only
// reads its own playbook.

const (
	// LessonTitleRunes bounds the title, which is half of an index line.
	LessonTitleRunes = 80
	// LessonCueRunes bounds the cue, the one line that says when to open it.
	LessonCueRunes = 160
	// LessonBodyRunes bounds the body, which is loaded only when opened.
	LessonBodyRunes = 4000
	// LessonIndexCap keeps the whole index small enough to show every time.
	LessonIndexCap = 24
)

var (
	// ErrLesson is a title, cue or body over its rune limit.
	ErrLesson = errors.New("memory: lesson too long")
	// ErrLessonTitle is an empty title.
	ErrLessonTitle = errors.New("memory: lesson needs a title")
	// ErrLessonCue is an empty cue.
	ErrLessonCue = errors.New("memory: lesson needs a cue")
	// ErrLessonBody is an empty body.
	ErrLessonBody = errors.New("memory: lesson needs a body")
	// ErrLessonDuplicate is a lesson the agent's playbook already holds.
	ErrLessonDuplicate = errors.New("memory: lesson already stored")
	// ErrLessonGone is a lesson that is missing or not visible to the caller.
	ErrLessonGone = errors.New("memory: lesson not found")
	// ErrLessonFull is a playbook already at LessonIndexCap.
	ErrLessonFull = errors.New("memory: playbook is full")
)

// Card is one index line. The index is the title and the cue, never the body.
type Card struct {
	ID    string
	Title string
	Cue   string
}

// Lesson is one opened lesson, body included.
type Lesson struct {
	ID    string
	Title string
	Cue   string
	Body  string
}

// Book is one transaction over agent playbooks.
type Book interface {
	Count(ctx context.Context, agentID string) (int, error)
	List(ctx context.Context, agentID string) ([]Card, error)
	One(ctx context.Context, agentID, lessonID string) (Lesson, error)
	Put(ctx context.Context, agentID, title, cue, body string, at time.Time) (string, error)
	Drop(ctx context.Context, agentID, lessonID string) error
}

// Playbook runs that transaction as an admin, who can read and write any
// playbook, or as one agent, who can only read its own.
type Playbook interface {
	InAdmin(ctx context.Context, fn func(Book) error) error
	InAgent(ctx context.Context, agentID string, fn func(Book) error) error
}

// Index lists an agent's cards. It loads no body, so it is cheap enough to
// show on every turn.
func Index(ctx context.Context, store Playbook, agentID string, asAdmin bool) ([]Card, error) {
	if _, err := uuid.Parse(agentID); err != nil {
		return nil, err
	}
	var cards []Card
	err := inBook(ctx, store, agentID, asAdmin, func(b Book) error {
		var err error
		cards, err = b.List(ctx, agentID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if cards == nil {
		cards = []Card{}
	}
	return cards, nil
}

// Open reads one lesson with its body. The body is loaded only here, when
// that lesson is opened.
func Open(ctx context.Context, store Playbook, agentID, lessonID string, asAdmin bool) (Lesson, error) {
	if _, err := uuid.Parse(agentID); err != nil {
		return Lesson{}, err
	}
	if _, err := uuid.Parse(lessonID); err != nil {
		return Lesson{}, err
	}
	var lesson Lesson
	err := inBook(ctx, store, agentID, asAdmin, func(b Book) error {
		var err error
		lesson, err = b.One(ctx, agentID, lessonID)
		return err
	})
	return lesson, err
}

// SaveLesson stores a lesson in an agent's playbook. Only an admin writes, so
// this never runs as the agent. The count and the insert share a transaction
// so the cap holds.
func SaveLesson(ctx context.Context, store Playbook, agentID, title, cue, body string, at time.Time) (string, error) {
	if _, err := uuid.Parse(agentID); err != nil {
		return "", err
	}
	title = strings.TrimSpace(title)
	cue = strings.TrimSpace(cue)
	body = strings.TrimSpace(body)
	switch {
	case title == "":
		return "", ErrLessonTitle
	case cue == "":
		return "", ErrLessonCue
	case body == "":
		return "", ErrLessonBody
	}
	if utf8.RuneCountInString(title) > LessonTitleRunes ||
		utf8.RuneCountInString(cue) > LessonCueRunes ||
		utf8.RuneCountInString(body) > LessonBodyRunes {
		return "", ErrLesson
	}
	title = domain.Redact(title)
	cue = domain.Redact(cue)
	body = domain.Redact(body)
	var id string
	err := store.InAdmin(ctx, func(b Book) error {
		n, err := b.Count(ctx, agentID)
		if err != nil {
			return err
		}
		if n >= LessonIndexCap {
			return ErrLessonFull
		}
		id, err = b.Put(ctx, agentID, title, cue, body, at)
		return err
	})
	return id, err
}

// RemoveLesson deletes a lesson. Only an admin writes. The row goes rather
// than staying as a tombstone, because nothing relearns a lesson.
func RemoveLesson(ctx context.Context, store Playbook, agentID, lessonID string) error {
	if _, err := uuid.Parse(agentID); err != nil {
		return err
	}
	if _, err := uuid.Parse(lessonID); err != nil {
		return err
	}
	return store.InAdmin(ctx, func(b Book) error {
		return b.Drop(ctx, agentID, lessonID)
	})
}

func inBook(ctx context.Context, store Playbook, agentID string, asAdmin bool, fn func(Book) error) error {
	if asAdmin {
		return store.InAdmin(ctx, fn)
	}
	return store.InAgent(ctx, agentID, fn)
}
