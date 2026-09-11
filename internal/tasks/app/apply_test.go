package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

const userID = "018f3b2c-7a10-7c4a-8e11-111111111111"

func TestApplyPassCallsTheBook(t *testing.T) {
	book := &fake{status: domain.Checking}
	_, err := ApplyTransition(context.Background(), book, Access{UserID: userID, Allow: "task:edit"}, taskOf(book), domain.Input{Kind: domain.Pass})
	if !errors.Is(err, domain.ErrEvidence) || book.calls != 1 {
		t.Fatalf("pass calls=%d err=%v", book.calls, err)
	}
	book.passes = true
	got, err := ApplyTransition(context.Background(), book, Access{UserID: userID, Allow: "task:edit"}, taskOf(book), domain.Input{Kind: domain.Pass})
	if err != nil || got.Status != domain.Done || book.calls != 2 {
		t.Fatalf("passed = %+v calls=%d err=%v", got, book.calls, err)
	}
}

func TestApplyOpensAndAdvances(t *testing.T) {
	book := &fake{}
	ctx := context.Background()
	access := Access{UserID: userID, Allow: "task:create,task:edit"}
	opened, err := ApplyTransition(ctx, book, access, "", domain.Input{Kind: domain.Open, Goal: "File the receipts"})
	if err != nil || opened.Status != domain.Preparing || opened.ID == "" {
		t.Fatalf("open = %+v %v", opened, err)
	}
	next, err := ApplyTransition(ctx, book, access, opened.ID, domain.Input{Kind: domain.Advance})
	if err != nil || next.Status != domain.Planning {
		t.Fatalf("advance = %+v %v", next, err)
	}
	noted, err := ApplyTransition(ctx, book, access, opened.ID, domain.Input{Kind: domain.Chat, Body: "the figures"})
	if err != nil || noted.Status != domain.Planning || book.chats != 1 {
		t.Fatalf("chat = %+v chats=%d %v", noted, book.chats, err)
	}
}

func TestApplyRejectsABadCaller(t *testing.T) {
	book := &fake{}
	ctx := context.Background()
	if _, err := ApplyTransition(ctx, book, Access{Allow: "task:edit"}, "", domain.Input{Kind: domain.Open, Goal: "x"}); !errors.Is(err, ErrAccess) || book.calls != 0 {
		t.Fatalf("user = %v calls=%d", err, book.calls)
	}
	if _, err := ApplyTransition(ctx, book, Access{UserID: userID}, "", domain.Input{Kind: domain.Open, Goal: "x"}); !errors.Is(err, ErrAccess) {
		t.Fatalf("allow = %v", err)
	}
	if _, err := ApplyTransition(ctx, book, Access{UserID: userID, Allow: "task:edit"}, "nope", domain.Input{Kind: domain.Advance}); !errors.Is(err, ErrTask) {
		t.Fatalf("task id = %v", err)
	}
	if _, err := ApplyTransition(ctx, book, Access{UserID: userID, Allow: "task:edit"}, "", domain.Input{Kind: domain.Advance}); !errors.Is(err, domain.ErrInput) {
		t.Fatalf("advance with no task = %v", err)
	}
}

func TestRecordChecksValidatesAndCallsTheBook(t *testing.T) {
	book := &fake{}
	ctx := context.Background()
	access := Access{UserID: userID, Allow: "task:edit"}
	taskID := taskOf(book)
	if _, err := RecordChecks(ctx, book, access, taskID, nil); !errors.Is(err, domain.ErrEvidence) || book.records != 0 {
		t.Fatalf("empty checks records=%d err=%v", book.records, err)
	}
	passed, err := RecordChecks(ctx, book, access, taskID, []domain.Check{{Name: "go test", Code: 0}})
	if err != nil || !passed || book.records != 1 {
		t.Fatalf("checks passed=%t records=%d err=%v", passed, book.records, err)
	}
	if _, err := RecordChecks(ctx, book, Access{Allow: "task:edit"}, taskID, []domain.Check{{Name: "go test"}}); !errors.Is(err, ErrAccess) {
		t.Fatalf("access = %v", err)
	}
	if _, err := RecordChecks(ctx, book, access, "nope", []domain.Check{{Name: "go test"}}); !errors.Is(err, ErrTask) {
		t.Fatalf("task = %v", err)
	}
}

func TestReviewContainsOnlyTheGivenDiffAndCriteria(t *testing.T) {
	got, err := domain.Review("the diff", "the criteria")
	if err != nil {
		t.Fatal(err)
	}
	const want = "criteria:\nthe criteria\n\ndiff:\nthe diff\n"
	if got != want {
		t.Fatalf("review = %q, want %q", got, want)
	}
	if strings.Contains(got, "private transcript") {
		t.Fatalf("review contains text that was not passed: %q", got)
	}
}

type fake struct {
	calls   int
	records int
	chats   int
	id      string
	status  domain.Status
	passes  bool
}

func (f *fake) Transition(_ context.Context, _ Access, taskID string, input domain.Input) (Task, error) {
	f.calls++
	if input.Kind == domain.Pass && !f.passes {
		return Task{}, domain.ErrEvidence
	}
	status := f.status
	if taskID == "" {
		status = ""
	}
	next, effect, err := domain.Step(status, input)
	if err != nil {
		return Task{}, err
	}
	if taskID == "" {
		f.id = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa"
		taskID = f.id
	}
	f.status = next
	if effect.Kind == domain.Chat {
		f.chats++
	}
	return Task{ID: taskID, Status: next, Goal: "File the receipts"}, nil
}

func (f *fake) Record(_ context.Context, _ Access, _ string, checks []domain.Check) (bool, error) {
	f.records++
	f.passes = domain.Passes(checks)
	return f.passes, nil
}

func taskOf(f *fake) string {
	if f.id == "" {
		return "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa"
	}
	return f.id
}
