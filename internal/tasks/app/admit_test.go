package app

import (
	"context"
	"strconv"
	"testing"

	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

const runTaskID = "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"

func TestThirdAdmitIsQueuedWithoutAnError(t *testing.T) {
	gate := &fakeGate{}
	access := Access{UserID: userID, Allow: "task:edit"}

	for index, want := range []domain.RunState{
		domain.RunAdmitted,
		domain.RunAdmitted,
		domain.RunQueued,
	} {
		got, err := Admit(context.Background(), gate, access, runTaskID, "")
		if err != nil || got.State != want {
			t.Fatalf("admit %d = %+v %v, want %s", index+1, got, err, want)
		}
	}
}

func TestSweepStopsWhenARunStaysQueued(t *testing.T) {
	gate := &fakeGate{
		queued: []string{
			"018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaa1",
			"018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaa2",
			"018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaa3",
		},
		rooms: []bool{true, false, true},
	}
	got, err := Sweep(context.Background(), gate, Access{UserID: userID, Allow: "task:edit"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != gate.queued[0] {
		t.Fatalf("sweep = %+v", got)
	}
	if gate.admitCalls != 2 {
		t.Fatalf("admit calls = %d, want 2", gate.admitCalls)
	}
}

func TestEndRunDoesNotListQueuedRuns(t *testing.T) {
	gate := &fakeGate{}
	got, err := EndRun(
		context.Background(),
		gate,
		Access{UserID: userID, Allow: "task:edit"},
		"018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaa1",
	)
	if err != nil || got.State != domain.RunEnded {
		t.Fatalf("end = %+v %v", got, err)
	}
	if gate.queuedCalls != 0 {
		t.Fatalf("queued calls = %d", gate.queuedCalls)
	}
}

type fakeGate struct {
	queued      []string
	rooms       []bool
	admitted    int
	admitCalls  int
	endCalls    int
	queuedCalls int
}

func (f *fakeGate) Admit(_ context.Context, _ Access, taskID, runID string) (Run, error) {
	room := domain.Room(f.admitted)
	if f.admitCalls < len(f.rooms) {
		room = f.rooms[f.admitCalls]
	}
	f.admitCalls++

	state := domain.RunState("")
	if runID == "" {
		runID = "018f3b2c-7a10-7c4a-8e11-ccccccccccc" + strconv.Itoa(f.admitCalls)
	} else {
		state = domain.RunQueued
	}
	next, err := domain.RunStep(state, domain.RunInput{Kind: domain.RunAdmit, Room: room})
	if err != nil {
		return Run{}, err
	}
	if next == domain.RunAdmitted {
		f.admitted++
	}
	if taskID == "" {
		taskID = runTaskID
	}
	return Run{ID: runID, TaskID: taskID, State: next}, nil
}

func (f *fakeGate) End(_ context.Context, _ Access, runID string) (Run, error) {
	f.endCalls++
	next, err := domain.RunStep(domain.RunAdmitted, domain.RunInput{Kind: domain.RunEnd})
	return Run{ID: runID, TaskID: runTaskID, State: next}, err
}

func (f *fakeGate) Queued(_ context.Context, _ Access) ([]string, error) {
	f.queuedCalls++
	return f.queued, nil
}
