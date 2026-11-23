package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/kernel"
	"github.com/upmo-ai/upmo/internal/platform/agentloop"
)

// openLessonTool opens one house rule of the reply's agent by id. It is only
// in the catalog of a reply that has an agent.
var openLessonTool = agentloop.ToolDef{
	Name:        "open_lesson",
	Description: "open one house rule of this agent by its id from the house rules index",
	Schema:      json.RawMessage(`{"type":"object","properties":{"lesson_id":{"type":"string"}},"required":["lesson_id"]}`),
	Category:    agentloop.CategoryDirect,
	Op:          agentloop.OpRead,
}

// catalog offers the core tools, and open_lesson when the reply has an agent.
func catalog(agent *Agent) *agentloop.ToolCatalog {
	if agent == nil {
		return agentloop.NewToolCatalog(nil, nil)
	}
	return agentloop.NewToolCatalog([]agentloop.ToolDef{openLessonTool}, nil)
}

// timedExecutor bounds one tool call.
type timedExecutor struct {
	inner agentloop.Executor
	limit time.Duration
}

// Execute runs inner under a deadline of t.limit. When that deadline passed
// and ctx itself did not end, the call is an error result, not a failure.
func (t timedExecutor) Execute(ctx context.Context, run *agentloop.RunState, call agentloop.ToolCall) (agentloop.ToolResult, error) {
	tctx, cancel := context.WithTimeout(ctx, t.limit)
	defer cancel()
	res, err := t.inner.Execute(tctx, run, call)
	if errors.Is(tctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return agentloop.ToolResult{Output: fmt.Sprintf("tool call timed out after %s", t.limit), IsError: true}, nil
	}
	return res, err
}

// lessonOpener opens a house rule with the access as it is now; errRevoked
// when the author's edit access is gone.
type lessonOpener struct {
	lookup LessonLookup
	live   *liveAccess
}

// OpenLesson implements agentloop.LessonOpener.
func (o lessonOpener) OpenLesson(ctx context.Context, agentID, lessonID string) (agentloop.Lesson, error) {
	current, ok := o.live.get()
	if !ok {
		return agentloop.Lesson{}, errRevoked
	}
	return o.lookup.OpenLesson(ctx, current, agentID, lessonID)
}

// outputs marks a failed save as a write failure; reads pass through. A
// failed read becomes a tool error result in RunTurn.
type outputs struct {
	inner agentloop.OutputStore
}

// SaveOutput implements agentloop.OutputStore.
func (o outputs) SaveOutput(ctx context.Context, run *agentloop.RunState, title, body string, untrusted bool) (string, error) {
	id, err := o.inner.SaveOutput(ctx, run, title, body, untrusted)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnwritten, err)
	}
	return id, nil
}

// RetrieveOutput implements agentloop.OutputStore.
func (o outputs) RetrieveOutput(ctx context.Context, run *agentloop.RunState, id string) (string, bool, error) {
	return o.inner.RetrieveOutput(ctx, run, id)
}

// roundStore is the chat agentloop.TurnStore of one round.
type roundStore struct {
	w        *replyWorker
	ctx      context.Context // the job's context; writes run on WithoutCancel of it
	claim    Claim
	live     *liveAccess
	sink     *deltaSink
	attempts *[]domain.Attempt
	done     bool
}

// CommitTurnAndEnqueue stores one round and queues the next, or stores the
// line when the round ends the reply.
func (s *roundStore) CommitTurnAndEnqueue(_ context.Context, commit agentloop.TurnCommit) error {
	tail, err := s.sink.finish()
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), finishTimeout)
	defer cancel()
	s.w.refresh(wctx, s.live, s.claim.Job)
	current, ok := s.live.get()
	if !ok {
		return errRevoked
	}
	rc := RoundCommit{Turn: commit, Tail: tail, End: RoundDone}
	round := s.claim.Job.Round
	switch {
	case commit.Status == agentloop.TurnBudgetExhausted:
		return kernel.ErrExhausted
	case commit.Status == agentloop.TurnContinue && round < MaxRounds:
		rc.End = RoundNext
	case commit.Status == agentloop.TurnContinue:
		rc.End, rc.Fail = RoundFailed, s.fail("rounds", commit.Model)
	case s.claim.BodyRunes == 0 && !s.sink.wrote():
		rc.End, rc.Fail = RoundFailed, s.fail("empty", commit.Model)
	}
	err = s.w.deps.Rows.CommitRound(wctx, current, s.claim, rc)
	switch {
	case err == nil:
		s.done = true
		return nil
	case errors.Is(err, ErrFenced), errors.Is(err, ErrGone), errors.Is(err, ErrSay), errors.Is(err, ErrHold):
		return err
	}
	return fmt.Errorf("%w: %w", ErrUnwritten, err)
}

func (s *roundStore) fail(class, model string) ReplyEnd {
	all := append(append([]domain.Attempt{}, s.claim.Attempts...), *s.attempts...)
	return ReplyEnd{Model: model, Attempts: closeAttempts(all, &ModelError{Class: class, Model: model})}
}
