//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/memory/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/memory/app"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestLessonAccessAndBodyLoading(t *testing.T) {
	owner := up(t)
	store := postgres.Facts{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	at := time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)

	firstID, err := app.SaveLesson(
		ctx, store, lessonAgentA, "Alpha", "When alpha applies", "Body SECRETWORD", at,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.SaveLesson(
		ctx, store, lessonAgentA, "Beta", "When beta applies", "Second body", at,
	); err != nil {
		t.Fatal(err)
	}
	otherID, err := app.SaveLesson(
		ctx, store, lessonAgentB, "Gamma", "When gamma applies", "Other body", at,
	)
	if err != nil {
		t.Fatal(err)
	}

	cards, err := app.Index(ctx, store, lessonAgentA, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 2 || cards[0].Title != "Alpha" || cards[1].Title != "Beta" {
		t.Fatalf("agent A cards = %+v", cards)
	}
	for _, card := range cards {
		if strings.Contains(card.Title, "SECRETWORD") || strings.Contains(card.Cue, "SECRETWORD") {
			t.Fatalf("body leaked into card: %+v", card)
		}
	}
	lesson, err := app.Open(ctx, store, lessonAgentA, firstID, false)
	if err != nil {
		t.Fatal(err)
	}
	if lesson.Body != "Body SECRETWORD" {
		t.Fatalf("body = %q", lesson.Body)
	}

	otherCards, err := app.Index(ctx, store, lessonAgentB, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(otherCards) != 1 || otherCards[0].Title != "Gamma" {
		t.Fatalf("agent B cards = %+v", otherCards)
	}
	if _, err := app.Open(ctx, store, lessonAgentA, otherID, false); !errors.Is(err, app.ErrLessonGone) {
		t.Fatalf("open another agent's lesson: %v", err)
	}

	adminCards, err := app.Index(ctx, store, lessonAgentA, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(adminCards) != 2 || adminCards[0].Title != "Alpha" || adminCards[1].Title != "Beta" {
		t.Fatalf("admin cards = %+v", adminCards)
	}
}

func TestLessonCap(t *testing.T) {
	owner := up(t)
	store := postgres.Facts{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	at := time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)

	for i := 1; i <= app.LessonIndexCap; i++ {
		if _, err := app.SaveLesson(
			ctx, store, lessonAgentA,
			fmt.Sprintf("Lesson %02d", i), "Use when needed", "Body", at,
		); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	if _, err := app.SaveLesson(
		ctx, store, lessonAgentA, "Lesson 25", "Use when needed", "Body", at,
	); !errors.Is(err, app.ErrLessonFull) {
		t.Fatalf("25th save: %v", err)
	}
	var count int
	if err := owner.QueryRow(ctx,
		`SELECT count(*) FROM memory.lessons WHERE agent_id = $1`,
		lessonAgentA,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != app.LessonIndexCap {
		t.Fatalf("lesson count = %d", count)
	}
}

func TestLessonTitleIsUniquePerAgent(t *testing.T) {
	owner := up(t)
	store := postgres.Facts{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	at := time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)

	if _, err := app.SaveLesson(
		ctx, store, lessonAgentA, "Shared title", "First cue", "First body", at,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SaveLesson(
		ctx, store, lessonAgentA, "Shared title", "Second cue", "Second body", at,
	); !errors.Is(err, app.ErrLessonDuplicate) {
		t.Fatalf("duplicate save: %v", err)
	}
	if _, err := app.SaveLesson(
		ctx, store, lessonAgentB, "Shared title", "Other cue", "Other body", at,
	); err != nil {
		t.Fatal(err)
	}
}

func TestLessonBodyIsRedacted(t *testing.T) {
	owner := up(t)
	store := postgres.Facts{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	at := time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)

	id, err := app.SaveLesson(
		ctx, store, lessonAgentA, "Contact", "When contact is needed",
		"Write to ada@example.com today", at,
	)
	if err != nil {
		t.Fatal(err)
	}
	lesson, err := app.Open(ctx, store, lessonAgentA, id, true)
	if err != nil {
		t.Fatal(err)
	}
	if lesson.Body != "Write to *** today" {
		t.Fatalf("body = %q", lesson.Body)
	}
}

func TestAgentCannotPutLesson(t *testing.T) {
	owner := up(t)
	store := postgres.Facts{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	at := time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)

	err := store.InAgent(ctx, lessonAgentA, func(book app.Book) error {
		_, err := book.Put(ctx, lessonAgentA, "Blocked", "Cue", "Body", at)
		return err
	})
	if err == nil {
		t.Fatal("agent put a lesson")
	}
	var count int
	if err := owner.QueryRow(ctx,
		`SELECT count(*) FROM memory.lessons WHERE agent_id = $1 AND title = 'Blocked'`,
		lessonAgentA,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("blocked lessons = %d", count)
	}
}

func TestRemoveLesson(t *testing.T) {
	owner := up(t)
	store := postgres.Facts{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	at := time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)

	id, err := app.SaveLesson(
		ctx, store, lessonAgentA, "Temporary", "Cue", "Body", at,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.RemoveLesson(ctx, store, lessonAgentA, id); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Open(ctx, store, lessonAgentA, id, true); !errors.Is(err, app.ErrLessonGone) {
		t.Fatalf("open removed lesson: %v", err)
	}
}

func TestEraseLeavesLessons(t *testing.T) {
	owner := up(t)
	store := postgres.Facts{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	at := time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)

	id, err := app.SaveLesson(
		ctx, store, lessonAgentA, "Durable", "Cue", "Body", at,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Erase(ctx, store, lessonEraseUser, ""); err != nil {
		t.Fatal(err)
	}
	lesson, err := app.Open(ctx, store, lessonAgentA, id, true)
	if err != nil {
		t.Fatal(err)
	}
	if lesson.ID != id {
		t.Fatalf("lesson after erase = %+v", lesson)
	}
}

const (
	lessonAgentA    = "018f3b2c-7a10-7c4a-8e11-aaaaaaa11111"
	lessonAgentB    = "018f3b2c-7a10-7c4a-8e11-bbbbbbb22222"
	lessonEraseUser = "018f3b2c-7a10-7c4a-8e11-111111111111"
)
