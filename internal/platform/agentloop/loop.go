package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/upmo-ai/upmo/internal/kernel"
	"github.com/upmo-ai/upmo/internal/platform/writeguard"
)

// Engine runs one turn of the shared agent loop. For DefaultExecutor values
// and pointers, a non-zero Engine.OffloadAtTokens takes precedence; when it is
// zero, RunTurn uses the executor's value. Wrapping Executors must set their
// own threshold.
type Engine struct {
	Model                ModelClient
	Executor             Executor
	WritePolicy          WritePolicy
	ActionLog            ActionLog
	TurnStore            TurnStore
	Outputs              OutputStore
	Audit                AuditSink
	Timeline             TimelineSink
	FallbackPrice        *kernel.TokenPrice
	FallbackCostMicroUSD int64
	OffloadAtTokens      int
	// ContextWindow is per Engine because each Engine serves one model.
	ContextWindow int
	// MaxToolCallsPerRound caps the calls of one model response that are run;
	// later calls get an error result only. 0 is off.
	MaxToolCallsPerRound int
	Now                  func() time.Time
}

// RunTurn executes a single model turn and its tool calls, enforcing micro-USD
// escrow, exposure tracking, writeguard tightening, deterministic batch holds,
// dry_run simulation, and 3-transaction at-most-once action execution.
func (e Engine) RunTurn(ctx context.Context, run *RunState, messages []Message, system string) (TurnOutcome, error) {
	if run == nil || e.Model == nil || e.Executor == nil {
		return TurnOutcome{}, ErrInvalidConfig
	}
	switch executor := e.Executor.(type) {
	case DefaultExecutor:
		if e.OffloadAtTokens == 0 {
			e.OffloadAtTokens = executor.OffloadAtTokens
		} else {
			executor.OffloadAtTokens = e.OffloadAtTokens
			e.Executor = executor
		}
	case *DefaultExecutor:
		if executor != nil {
			configured := *executor
			if e.OffloadAtTokens == 0 {
				e.OffloadAtTokens = configured.OffloadAtTokens
			} else {
				configured.OffloadAtTokens = e.OffloadAtTokens
			}
			e.Executor = configured
		}
	}
	if run.Paused {
		return TurnOutcome{}, kernel.ErrExhausted
	}
	if run.Budgeted && (run.BudgetMicroUSD <= 0 || kernel.Remaining(run.BudgetMicroUSD, run.SpentMicroUSD) <= 0) {
		return TurnOutcome{}, kernel.ErrExhausted
	}
	if run.Catalog == nil {
		run.Catalog = NewToolCatalog(nil, nil)
	}
	nowFn := e.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}

	if e.ContextWindow > 0 {
		tools := run.Catalog.PromptTools()
		available := PromptMessageBudget(e.ContextWindow, system, tools)
		trimmed, trimErr := TrimMessages(ctx, e.Outputs, run, messages, e.ContextWindow, available)
		if trimErr != nil {
			return TurnOutcome{}, trimErr
		}
		messages = trimmed
		if estimateMessages(messages) > available {
			return TurnOutcome{}, ErrContextExceeded
		}
	}
	req := BuildPromptRequest(run, system, messages)
	resp, err := e.Model.Complete(ctx, req)
	if err != nil {
		return TurnOutcome{}, err
	}
	if resp.ContextWindow > 0 {
		e.ContextWindow = resp.ContextWindow
	}

	price := resp.Price
	if price == nil {
		price = e.FallbackPrice
	}
	charge, estimated, err := kernel.ResolveRunCharge(run.Budgeted, resp.CostMicroUSD, resp.Usage, price, e.FallbackCostMicroUSD)
	if err != nil {
		return TurnOutcome{}, err
	}
	if run.Budgeted {
		nextSpent, nextPaused, cerr := kernel.ChargeActual(run.BudgetMicroUSD, run.SpentMicroUSD, charge, run.Paused)
		if cerr != nil {
			return TurnOutcome{}, cerr
		}
		run.SpentMicroUSD = nextSpent
		run.Paused = nextPaused
	} else {
		run.SpentMicroUSD += charge
	}

	outcome := TurnOutcome{
		History:       cloneMessages(messages),
		AssistantText: resp.Text,
		Model:         resp.Model,
		CostMicroUSD:  charge,
		EstimatedCost: estimated,
		SpentMicroUSD: run.SpentMicroUSD,
		Paused:        run.Paused,
	}

	assistantMsg := Message{
		Role:      RoleAssistant,
		Text:      resp.Text,
		ToolCalls: resp.ToolCalls,
	}
	appended := []Message{assistantMsg}

	approvedCalls := maps.Clone(run.ApprovedCalls)
	var heldCalls []HeldCall
	for i, call := range resp.ToolCalls {
		if e.MaxToolCallsPerRound > 0 && i >= e.MaxToolCallsPerRound {
			appended = append(appended, Message{
				Role:     RoleTool,
				ToolName: call.Name,
				ToolID:   call.ID,
				Text:     fmt.Sprintf("skipped: more than %d tool calls in one round", e.MaxToolCallsPerRound),
				IsError:  true,
			})
			continue
		}
		def, ok := run.Catalog.Get(call.Name)
		if !ok {
			action, preview := RenderCallPreview(call.Name, ToolOp("unknown"), call.Arguments)
			ev := TimelineEvent{
				Kind:     EventBlocked,
				CallID:   call.ID,
				ToolName: call.Name,
				Reason:   "unknown_tool",
				Action:   action,
				Preview:  preview,
				At:       nowFn(),
			}
			outcome.BlockedEvents = append(outcome.BlockedEvents, ev)
			if err := e.emitAndAudit(ctx, run, ev); err != nil {
				return TurnOutcome{}, err
			}
			appended = append(appended, Message{
				Role:     RoleTool,
				ToolName: call.Name,
				ToolID:   call.ID,
				Text:     "unknown or inactive tool",
				IsError:  true,
			})
			continue
		}

		isMutating := def.Op == OpChange || def.Op == OpDelete || (def.External && def.Op != OpRead)
		if isMutating {
			verdict, target, reason, err := e.evaluateWriteVerdict(ctx, run, call, def)
			if err != nil {
				return TurnOutcome{}, err
			}
			if verdict == "block" {
				action, preview := RenderCallPreview(call.Name, def.Op, call.Arguments)
				ev := TimelineEvent{
					Kind:            EventBlocked,
					CallID:          call.ID,
					ToolName:        call.Name,
					Reason:          firstNonEmpty(reason, "blocked"),
					Action:          action,
					Preview:         preview,
					UntrustedOrigin: run.Exposure.Tainted && len(DetectNovelSinks(call.Arguments)) > 0,
					At:              nowFn(),
				}
				outcome.BlockedEvents = append(outcome.BlockedEvents, ev)
				if err := e.emitAndAudit(ctx, run, ev); err != nil {
					return TurnOutcome{}, err
				}
				appended = append(appended, Message{
					Role:     RoleTool,
					ToolName: call.Name,
					ToolID:   call.ID,
					Text:     fmt.Sprintf("blocked: %s", ev.Reason),
					IsError:  true,
				})
				continue
			}

			if run.DryRun {
				held := BuildHeldCall(call, def, target, run.Exposure.Tainted)
				action, preview, synthetic := DryRunResult(call.Name, def.Op, call.Arguments, held.UntrustedOrigin)
				// Only external mutating calls hit ActionLog; internal writes still honor DryRun simulation.
				if def.External && e.ActionLog != nil {
					if rerr := e.ActionLog.RecordSimulated(ctx, ActionRecord{
						CallID:          ScopedCallID(run.Seq, call.ID),
						RunID:           run.RunID,
						ObjectID:        run.ObjectID,
						UserID:          run.UserID,
						ToolName:        call.Name,
						Action:          action,
						Preview:         preview,
						State:           ActionStateSimulated,
						UntrustedOrigin: held.UntrustedOrigin,
						DryRun:          true,
						ExecutedAt:      nowFn(),
					}); rerr != nil {
						return TurnOutcome{}, rerr
					}
				}
				ev := TimelineEvent{
					Kind:            EventSimulated,
					CallID:          call.ID,
					ToolName:        call.Name,
					Reason:          "dry_run",
					Action:          action,
					Preview:         preview,
					UntrustedOrigin: held.UntrustedOrigin,
					DryRun:          true,
					At:              nowFn(),
				}
				outcome.SimulatedEvents = append(outcome.SimulatedEvents, ev)
				if err := e.emitAndAudit(ctx, run, ev); err != nil {
					return TurnOutcome{}, err
				}
				appended = append(appended, Message{
					Role:     RoleTool,
					ToolName: call.Name,
					ToolID:   call.ID,
					Text:     synthetic,
				})
				continue
			}

			held := BuildHeldCall(call, def, target, run.Exposure.Tainted)
			if verdict == string(writeguard.Ask) {
				expectedDigest, approved := approvedCalls[call.ID]
				if !approved || expectedDigest == "" || expectedDigest != held.ArgsDigest {
					heldCalls = append(heldCalls, held)
					ev := TimelineEvent{
						Kind:            EventBlocked,
						CallID:          call.ID,
						ToolName:        call.Name,
						Reason:          "ask",
						Action:          held.Action,
						Preview:         held.Preview,
						UntrustedOrigin: held.UntrustedOrigin,
						At:              nowFn(),
					}
					outcome.BlockedEvents = append(outcome.BlockedEvents, ev)
					if err := e.emitAndAudit(ctx, run, ev); err != nil {
						return TurnOutcome{}, err
					}
					appended = append(appended, Message{
						Role:            RoleTool,
						ToolName:        call.Name,
						ToolID:          call.ID,
						Text:            "awaiting approval",
						HeldPlaceholder: true,
					})
					continue
				}
				delete(approvedCalls, call.ID)
			}

			if def.External {
				// Allowed (or explicitly approved): enforce 3-tx at-most-once execution.
				if e.ActionLog == nil {
					return TurnOutcome{}, fmt.Errorf("%w: ActionLog required for mutating external write", ErrInvalidConfig)
				}
				var res ToolResult
				err = ExecuteAtMostOnce(ctx, e.ActionLog, ActionRecord{
					CallID:          ScopedCallID(run.Seq, call.ID),
					RunID:           run.RunID,
					ObjectID:        run.ObjectID,
					UserID:          run.UserID,
					ToolName:        call.Name,
					Action:          held.Action,
					Preview:         held.Preview,
					UntrustedOrigin: held.UntrustedOrigin,
					ExecutedAt:      nowFn(),
				}, func(ctx context.Context, idempotencyKey string) (string, error) {
					callWithKey := call
					callWithKey.IdempotencyKey = idempotencyKey
					var execErr error
					res, execErr = e.Executor.Execute(ctx, run, callWithKey)
					return res.ResponseDigest, execErr
				})
				if errors.Is(err, ErrDuplicateAction) {
					ev := TimelineEvent{
						Kind:     EventBlocked,
						CallID:   call.ID,
						ToolName: call.Name,
						Reason:   "duplicate_call_id",
						Action:   held.Action,
						Preview:  held.Preview,
						At:       nowFn(),
					}
					outcome.BlockedEvents = append(outcome.BlockedEvents, ev)
					if eerr := e.emitAndAudit(ctx, run, ev); eerr != nil {
						return TurnOutcome{}, eerr
					}
					appended = append(appended, Message{
						Role:     RoleTool,
						ToolName: call.Name,
						ToolID:   call.ID,
						Text:     err.Error(),
						IsError:  true,
					})
					continue
				}
				if err != nil && res.Output == "" {
					res.Output = err.Error()
					res.IsError = true
				}
				msg, oerr := e.finalizeToolResult(ctx, run, call, res)
				if oerr != nil {
					return TurnOutcome{}, oerr
				}
				appended = append(appended, msg)
				continue
			}
		}

		// Non-external or read-only tool execution.
		res, execErr := e.Executor.Execute(ctx, run, call)
		if execErr != nil && res.Output == "" {
			res.Output = execErr.Error()
			res.IsError = true
		}
		msg, oerr := e.finalizeToolResult(ctx, run, call, res)
		if oerr != nil {
			return TurnOutcome{}, oerr
		}
		appended = append(appended, msg)
	}

	outcome.Exposure = run.Exposure
	outcome.Appended = appended
	outcome.History = append(outcome.History, appended...)

	// The tools are read after the loop so discovered tools count. The newest
	// assistant message holds this turn's calls, so the trim keeps this turn's
	// results; it may save outputs, which all return before tx3 opens.
	if e.ContextWindow > 0 {
		budget := PostTurnBudget(e.ContextWindow, system, run.Catalog.PromptTools())
		trimmed, err := TrimMessages(ctx, e.Outputs, run, outcome.History, e.ContextWindow, budget)
		if err != nil {
			return TurnOutcome{}, err
		}
		outcome.History = trimmed
	}

	enqueueNext := false
	switch {
	case len(heldCalls) > 0:
		batch := BuildBatchHold("ask", heldCalls)
		outcome.Hold = &batch
		outcome.Status = TurnWaitingApproval
	case run.Paused:
		outcome.Status = TurnBudgetExhausted
	case len(resp.ToolCalls) > 0:
		outcome.Status = TurnContinue
		enqueueNext = true
	default:
		outcome.Status = TurnDone
	}

	if e.TurnStore != nil {
		commit := TurnCommit{
			RunID:                run.RunID,
			ObjectID:             run.ObjectID,
			UserID:               run.UserID,
			SeqStart:             run.Seq + 1,
			Messages:             appended,
			History:              outcome.History,
			Status:               outcome.Status,
			Model:                resp.Model,
			InputTokens:          int(resp.Usage.InputTokens),
			OutputTokens:         int(resp.Usage.OutputTokens),
			CostMicroUSD:         charge,
			SpentMicroUSD:        run.SpentMicroUSD,
			Paused:               run.Paused,
			Exposure:             run.Exposure,
			Hold:                 outcome.Hold,
			BlockedEvents:        outcome.BlockedEvents,
			SimulatedEvents:      outcome.SimulatedEvents,
			EnqueueNext:          enqueueNext,
			ReportedCostMicroUSD: resp.CostMicroUSD,
		}
		if err := e.TurnStore.CommitTurnAndEnqueue(ctx, commit); err != nil {
			return TurnOutcome{}, err
		}
		run.Seq += len(appended)
	}
	run.ApprovedCalls = approvedCalls
	return outcome, nil
}

func (e Engine) evaluateWriteVerdict(ctx context.Context, run *RunState, call ToolCall, def ToolDef) (verdict, target, reason string, err error) {
	base := string(writeguard.Ask)
	if e.WritePolicy != nil {
		dec, derr := e.WritePolicy.Decide(ctx, run.UserID, call.Name, def.Op, call.Arguments)
		if derr != nil {
			return "", "", "", derr
		}
		target = dec.Resource
		reason = dec.Reason
		switch dec.Verdict {
		case "":
			// Keep default "ask".
		case string(writeguard.Allow), string(writeguard.Ask), "block":
			base = dec.Verdict
		default:
			if reason == "" {
				reason = "invalid_policy_verdict"
			}
			return "block", target, reason, nil
		}
	}
	tight := writeguard.Tighten(base, writeguard.Exposure{
		Untrusted: run.Exposure.Tainted,
		Private:   run.Exposure.PrivateRead,
		External:  def.External,
	})
	if tight == string(writeguard.Ask) && base != string(writeguard.Ask) && reason == "" {
		reason = "untrusted_write_guard"
	}
	return tight, target, reason, nil
}

func (e Engine) finalizeToolResult(ctx context.Context, run *RunState, call ToolCall, res ToolResult) (Message, error) {
	run.Exposure.Observe(res.Untrusted, res.Private)
	if len(res.DiscoveredTools) > 0 && run.Catalog != nil {
		run.Catalog.Append(res.DiscoveredTools...)
	}
	text, offloaded, err := res.Output, false, error(nil)
	if call.Name != toolRetrieveOutput {
		text, offloaded, err = MaybeOffload(ctx, e.Outputs, run, call.Name, res.Output, res.Untrusted, e.OffloadAtTokens)
	}
	if err != nil {
		return Message{}, err
	}
	return Message{
		Role:     RoleTool,
		ToolName: call.Name,
		ToolID:   call.ID,
		Text:     text,
		IsError:  res.IsError,
		Offload:  offloaded,
	}, nil
}

func (e Engine) emitAndAudit(ctx context.Context, run *RunState, ev TimelineEvent) error {
	if e.Timeline != nil {
		if err := e.Timeline.Emit(ctx, run, ev); err != nil {
			return err
		}
	}
	if e.Audit != nil {
		if err := e.Audit.Record(ctx, run, ev); err != nil {
			return err
		}
	}
	return nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// MarshalBatchHoldJSON encodes calls as JSON for storage or inspection.
func MarshalBatchHoldJSON(h BatchHold) (json.RawMessage, error) {
	return json.Marshal(h)
}
