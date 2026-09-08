package app

import (
	"context"
	"errors"
	"testing"
)

const testTask = "018f3b2c-7a10-7c4a-8e11-333333333333"

func TestEraseRejectsABadUserID(t *testing.T) {
	store := &eraseStore{}
	if err := Erase(context.Background(), store, "not-a-uuid", ""); err == nil || store.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, store.calls)
	}
}

func TestEraseRejectsABadTaskID(t *testing.T) {
	store := &eraseStore{}
	if err := Erase(context.Background(), store, testUser, "not-a-uuid"); err == nil || store.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, store.calls)
	}
}

func TestErasePassesAnEmptyTaskIDThrough(t *testing.T) {
	store := &eraseStore{}
	if err := Erase(context.Background(), store, testUser, ""); err != nil {
		t.Fatal(err)
	}
	if store.calls != 1 || store.userID != testUser || store.taskID != "" {
		t.Fatalf("calls=%d user=%s task=%q", store.calls, store.userID, store.taskID)
	}
}

func TestErasePassesATaskIDThrough(t *testing.T) {
	store := &eraseStore{}
	if err := Erase(context.Background(), store, testUser, testTask); err != nil {
		t.Fatal(err)
	}
	if store.calls != 1 || store.userID != testUser || store.taskID != testTask {
		t.Fatalf("calls=%d user=%s task=%q", store.calls, store.userID, store.taskID)
	}
}

func TestEraseReturnsTheStoreError(t *testing.T) {
	want := errors.New("store")
	store := &eraseStore{err: want}
	if err := Erase(context.Background(), store, testUser, ""); err != want {
		t.Fatalf("err=%v", err)
	}
}

type eraseStore struct {
	calls  int
	userID string
	taskID string
	err    error
}

func (s *eraseStore) Erase(_ context.Context, userID, taskID string) error {
	s.calls++
	s.userID = userID
	s.taskID = taskID
	return s.err
}
