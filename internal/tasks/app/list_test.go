package app

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

type fakeLister struct {
	calls int
	tasks []TaskSummary
}

func (f *fakeLister) List(context.Context, Access) ([]TaskSummary, error) {
	f.calls++
	return append([]TaskSummary(nil), f.tasks...), nil
}

func TestListTasks(t *testing.T) {
	lister := &fakeLister{tasks: []TaskSummary{{ID: "6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b", Title: "File the receipts", State: domain.Working, Status: domain.ShownWorking, Phase: domain.Working}}}
	got, err := ListTasks(context.Background(), lister, Access{UserID: "6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b", Allow: "task:view"})
	if err != nil || len(got) != 1 || lister.calls != 1 {
		t.Fatalf("got %#v err %v calls %d", got, err, lister.calls)
	}
	if _, err := ListTasks(context.Background(), lister, Access{UserID: "bad", Allow: "task:view"}); !errors.Is(err, ErrAccess) || lister.calls != 1 {
		t.Fatalf("bad user = %v calls %d", err, lister.calls)
	}
	if _, err := ListTasks(context.Background(), lister, Access{UserID: "6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b"}); !errors.Is(err, ErrAccess) || lister.calls != 1 {
		t.Fatalf("empty allow = %v calls %d", err, lister.calls)
	}
}
