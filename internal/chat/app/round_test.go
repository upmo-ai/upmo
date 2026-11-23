package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/kernel"
	"github.com/upmo-ai/upmo/internal/platform/agentloop"
)

func TestCatalogExcludesPrivateSearchToolsWhenShared(t *testing.T) {
	for _, tc := range []struct {
		private                   bool
		wantMemory, wantKnowledge bool
	}{{true, true, true}, {false, false, false}} {
		got := catalog(nil, tc.private, true, true)
		_, memoryOK := got.Get("search_memory")
		_, knowledgeOK := got.Get("search_knowledge")
		if memoryOK != tc.wantMemory || knowledgeOK != tc.wantKnowledge {
			t.Fatalf("private=%v tools: memory=%v knowledge=%v", tc.private, memoryOK, knowledgeOK)
		}
	}
}

// blockingExecutor blocks until its context ends.
type blockingExecutor struct{}

func (blockingExecutor) Execute(ctx context.Context, _ *agentloop.RunState, _ agentloop.ToolCall) (agentloop.ToolResult, error) {
	<-ctx.Done()
	return agentloop.ToolResult{}, ctx.Err()
}

func TestATimedOutToolCallIsAnErrorResult(t *testing.T) {
	inner := timedExecutor{limit: 20 * time.Millisecond, inner: blockingExecutor{}}
	// The test's own bound: without the call's deadline the parent ends first
	// and the call fails promptly instead of hanging.
	parent, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	res, err := inner.Execute(parent, &agentloop.RunState{}, agentloop.ToolCall{Name: "find_tool"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !res.IsError || res.Output != "tool call timed out after 20ms" {
		t.Fatalf("result = %+v", res)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("the timeout took %v", time.Since(start))
	}
}

func TestFailureOfNamesTheClass(t *testing.T) {
	if got := failureOf(context.Background(), kernel.ErrExhausted); got.Class != "limit_reached" {
		t.Fatalf("ErrExhausted = %q", got.Class)
	}
	if got := failureOf(context.Background(), ErrHold); got.Class != "hold" {
		t.Fatalf("ErrHold = %q", got.Class)
	}
	if got := failureOf(context.Background(), fmt.Errorf("x: %w", ErrUnwritten)); got.Class != "unwritten" {
		t.Fatalf("ErrUnwritten = %q", got.Class)
	}
	deadline, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if got := failureOf(deadline, errors.New("x")); got.Class != "timeout" {
		t.Fatalf("expired ctx = %q", got.Class)
	}
}
