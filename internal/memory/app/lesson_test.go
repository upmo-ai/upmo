package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	testAgent  = "018f3b2c-7a10-7c4a-8e11-aaaaaaa11111"
	testLesson = "018f3b2c-7a10-7c4a-8e11-ccccccc33333"
)

func TestLessonBadAgentIDDoesNotCallTheStore(t *testing.T) {
	ctx := context.Background()
	calls := map[string]func(*lessonStore) error{
		"Index": func(s *lessonStore) error {
			_, err := Index(ctx, s, "not-a-uuid", false)
			return err
		},
		"Open": func(s *lessonStore) error {
			_, err := Open(ctx, s, "not-a-uuid", testLesson, false)
			return err
		},
		"SaveLesson": func(s *lessonStore) error {
			_, err := SaveLesson(ctx, s, "not-a-uuid", "Title", "Cue", "Body", time.Now())
			return err
		},
		"RemoveLesson": func(s *lessonStore) error {
			return RemoveLesson(ctx, s, "not-a-uuid", testLesson)
		},
	}
	for name, call := range calls {
		store := &lessonStore{}
		if err := call(store); err == nil || store.calls() != 0 {
			t.Fatalf("%s: err=%v calls=%d", name, err, store.calls())
		}
	}
}

func TestLessonBadLessonIDDoesNotCallTheStore(t *testing.T) {
	store := &lessonStore{}
	if _, err := Open(context.Background(), store, testAgent, "not-a-uuid", true); err == nil || store.calls() != 0 {
		t.Fatalf("Open: err=%v calls=%d", err, store.calls())
	}
	if err := RemoveLesson(context.Background(), store, testAgent, "not-a-uuid"); err == nil || store.calls() != 0 {
		t.Fatalf("RemoveLesson: err=%v calls=%d", err, store.calls())
	}
}

func TestLessonEmptyFieldDoesNotCallTheStore(t *testing.T) {
	cases := []struct {
		title, cue, body string
		want             error
	}{
		{"  ", "Cue", "Body", ErrLessonTitle},
		{"Title", "  ", "Body", ErrLessonCue},
		{"Title", "Cue", "  ", ErrLessonBody},
	}
	for _, c := range cases {
		store := &lessonStore{}
		_, err := SaveLesson(context.Background(), store, testAgent, c.title, c.cue, c.body, time.Now())
		if !errors.Is(err, c.want) || store.calls() != 0 {
			t.Fatalf("want=%v err=%v calls=%d", c.want, err, store.calls())
		}
	}
}

func TestLessonOverTheRuneLimitDoesNotCallTheStore(t *testing.T) {
	cases := []struct {
		name, title, cue, body string
	}{
		{"title", strings.Repeat("é", LessonTitleRunes+1), "Cue", "Body"},
		{"cue", "Title", strings.Repeat("é", LessonCueRunes+1), "Body"},
		{"body", "Title", "Cue", strings.Repeat("é", LessonBodyRunes+1)},
	}
	for _, c := range cases {
		store := &lessonStore{}
		_, err := SaveLesson(context.Background(), store, testAgent, c.title, c.cue, c.body, time.Now())
		if !errors.Is(err, ErrLesson) || store.calls() != 0 {
			t.Fatalf("%s: err=%v calls=%d", c.name, err, store.calls())
		}
	}
}

func TestLessonAtTheRuneLimitCountsRunesNotBytes(t *testing.T) {
	store := &lessonStore{book: &lessonBook{id: testLesson}}
	_, err := SaveLesson(context.Background(), store, testAgent,
		strings.Repeat("é", LessonTitleRunes),
		strings.Repeat("é", LessonCueRunes),
		strings.Repeat("é", LessonBodyRunes),
		time.Now())
	if err != nil || store.book.puts != 1 {
		t.Fatalf("err=%v puts=%d", err, store.book.puts)
	}
}

func TestLessonSaveRedactsAnEmailBeforePut(t *testing.T) {
	store := &lessonStore{book: &lessonBook{id: testLesson}}
	_, err := SaveLesson(context.Background(), store, testAgent,
		" Ask ada@example.com ",
		" When ada@example.com is named ",
		" Write to ada@example.com first. ",
		time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b := store.book
	if b.title != "Ask ***" || b.cue != "When *** is named" || b.body != "Write to *** first." {
		t.Fatalf("title=%q cue=%q body=%q", b.title, b.cue, b.body)
	}
}

func TestLessonSaveRejectsAFullPlaybook(t *testing.T) {
	store := &lessonStore{book: &lessonBook{count: LessonIndexCap}}
	_, err := SaveLesson(context.Background(), store, testAgent, "Title", "Cue", "Body", time.Now())
	if !errors.Is(err, ErrLessonFull) || store.book.counts != 1 || store.book.puts != 0 {
		t.Fatalf("err=%v counts=%d puts=%d", err, store.book.counts, store.book.puts)
	}
}

func TestLessonSavePutsBelowTheCap(t *testing.T) {
	store := &lessonStore{book: &lessonBook{count: LessonIndexCap - 1, id: testLesson}}
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	id, err := SaveLesson(context.Background(), store, testAgent, "Title", "Cue", "Body", at)
	if err != nil {
		t.Fatal(err)
	}
	b := store.book
	if id != testLesson || b.puts != 1 || b.agentID != testAgent || !b.at.Equal(at) {
		t.Fatalf("id=%s puts=%d agent=%s at=%v", id, b.puts, b.agentID, b.at)
	}
}

func TestLessonSaveRunsAsAdminOnly(t *testing.T) {
	store := &lessonStore{book: &lessonBook{id: testLesson}}
	if _, err := SaveLesson(context.Background(), store, testAgent, "Title", "Cue", "Body", time.Now()); err != nil {
		t.Fatal(err)
	}
	if store.admin != 1 || store.agent != 0 {
		t.Fatalf("admin=%d agent=%d", store.admin, store.agent)
	}
}

func TestLessonIndexAsAgentRunsInAgent(t *testing.T) {
	store := &lessonStore{book: &lessonBook{cards: []Card{{ID: testLesson, Title: "Title", Cue: "Cue"}}}}
	cards, err := Index(context.Background(), store, testAgent, false)
	if err != nil {
		t.Fatal(err)
	}
	if store.agent != 1 || store.admin != 0 || store.agentID != testAgent {
		t.Fatalf("admin=%d agent=%d agentID=%s", store.admin, store.agent, store.agentID)
	}
	if len(cards) != 1 || cards[0].ID != testLesson || store.book.agentID != testAgent {
		t.Fatalf("cards=%v listed=%s", cards, store.book.agentID)
	}
}

func TestLessonIndexAsAdminRunsInAdmin(t *testing.T) {
	store := &lessonStore{}
	if _, err := Index(context.Background(), store, testAgent, true); err != nil {
		t.Fatal(err)
	}
	if store.admin != 1 || store.agent != 0 || store.book.lists != 1 || store.book.agentID != testAgent {
		t.Fatalf("admin=%d agent=%d lists=%d listed=%s", store.admin, store.agent, store.book.lists, store.book.agentID)
	}
}

func TestLessonIndexTurnsANilListIntoAnEmptySlice(t *testing.T) {
	store := &lessonStore{}
	cards, err := Index(context.Background(), store, testAgent, false)
	if err != nil {
		t.Fatal(err)
	}
	if cards == nil || len(cards) != 0 {
		t.Fatalf("cards=%#v", cards)
	}
}

func TestLessonOpenPassesTheIDsThrough(t *testing.T) {
	want := Lesson{ID: testLesson, Title: "Title", Cue: "Cue", Body: "Body"}
	store := &lessonStore{book: &lessonBook{lesson: want}}
	got, err := Open(context.Background(), store, testAgent, testLesson, false)
	if err != nil {
		t.Fatal(err)
	}
	b := store.book
	if got != want || b.ones != 1 || b.agentID != testAgent || b.lessonID != testLesson {
		t.Fatalf("got=%v ones=%d agent=%s lesson=%s", got, b.ones, b.agentID, b.lessonID)
	}
}

func TestLessonRemovePassesTheIDsThrough(t *testing.T) {
	store := &lessonStore{}
	if err := RemoveLesson(context.Background(), store, testAgent, testLesson); err != nil {
		t.Fatal(err)
	}
	b := store.book
	if store.admin != 1 || store.agent != 0 || b.drops != 1 || b.agentID != testAgent || b.lessonID != testLesson {
		t.Fatalf("admin=%d agent=%d drops=%d agent=%s lesson=%s", store.admin, store.agent, b.drops, b.agentID, b.lessonID)
	}
}

type lessonStore struct {
	book    *lessonBook
	admin   int
	agent   int
	agentID string
}

func (s *lessonStore) calls() int { return s.admin + s.agent }

func (s *lessonStore) InAdmin(_ context.Context, fn func(Book) error) error {
	s.admin++
	return fn(s.ensureBook())
}

func (s *lessonStore) InAgent(_ context.Context, agentID string, fn func(Book) error) error {
	s.agent++
	s.agentID = agentID
	return fn(s.ensureBook())
}

func (s *lessonStore) ensureBook() *lessonBook {
	if s.book == nil {
		s.book = &lessonBook{}
	}
	return s.book
}

type lessonBook struct {
	count  int
	cards  []Card
	lesson Lesson
	id     string

	counts, lists, ones, puts, drops int

	agentID, lessonID string
	title, cue, body  string
	at                time.Time
}

func (b *lessonBook) Count(_ context.Context, agentID string) (int, error) {
	b.counts++
	b.agentID = agentID
	return b.count, nil
}

func (b *lessonBook) List(_ context.Context, agentID string) ([]Card, error) {
	b.lists++
	b.agentID = agentID
	return b.cards, nil
}

func (b *lessonBook) One(_ context.Context, agentID, lessonID string) (Lesson, error) {
	b.ones++
	b.agentID = agentID
	b.lessonID = lessonID
	return b.lesson, nil
}

func (b *lessonBook) Put(_ context.Context, agentID, title, cue, body string, at time.Time) (string, error) {
	b.puts++
	b.agentID = agentID
	b.title, b.cue, b.body, b.at = title, cue, body, at
	return b.id, nil
}

func (b *lessonBook) Drop(_ context.Context, agentID, lessonID string) error {
	b.drops++
	b.agentID = agentID
	b.lessonID = lessonID
	return nil
}
