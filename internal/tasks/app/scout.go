package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

const (
	scoutTriggerChat      = "chat"
	scoutTriggerTask      = "task"
	scoutTriggerKnowledge = "knowledge"

	scoutPromptMax   = 12_000
	scoutChatLines   = 40
	scoutProposeWait = 30 * time.Second
)

// SourceRef names one persisted source the scout read.
type SourceRef struct {
	Kind string
	ID   string
}

// OpenGoal is one non-terminal task goal for a person.
type OpenGoal struct {
	ID   string
	Goal string
}

// GoalsLister lists open task goals for one person.
type GoalsLister interface {
	ListOpenGoals(ctx context.Context, userID uuid.UUID) ([]OpenGoal, error)
}

// ChatReader returns recent chat lines when the trigger is chat.
type ChatReader interface {
	RecentLines(ctx context.Context, userID uuid.UUID, chatID string, limit int) ([]string, error)
}

// Proposer is one gateway chat call for title proposals.
type Proposer interface {
	Propose(ctx context.Context, prompt string) (string, error)
}

// NoticeWriter persists proposal notices.
type NoticeWriter interface {
	Insert(ctx context.Context, userID uuid.UUID, kind, title string, taskID *uuid.UUID) (uuid.UUID, error)
}

// ScoutArgs is one scout run.
type ScoutArgs struct {
	UserID   uuid.UUID
	Trigger  string
	SourceID string
}

// ScoutDeps are the ports RunScout needs.
type ScoutDeps struct {
	Goals   GoalsLister
	Chat    ChatReader
	Propose func(ctx context.Context, userID uuid.UUID) Proposer
	Notices NoticeWriter
}

// ErrRetry is a transient gateway failure River should retry.
var ErrRetry = errors.New("tasks: scout retry")

// RunScout gathers context, proposes titles, gates them, and writes proposal notices.
// Audit lists the persisted source ids the run read.
func RunScout(ctx context.Context, deps ScoutDeps, args ScoutArgs) (int, []SourceRef, error) {
	if args.UserID == uuid.Nil {
		return 0, nil, fmt.Errorf("tasks: scout: user")
	}
	if deps.Goals == nil || deps.Propose == nil || deps.Notices == nil {
		return 0, nil, fmt.Errorf("tasks: scout: deps")
	}
	var audit []SourceRef

	openGoals, err := deps.Goals.ListOpenGoals(ctx, args.UserID)
	if err != nil {
		return 0, nil, err
	}
	goalTitles := make([]string, 0, len(openGoals))
	for _, row := range openGoals {
		goalTitles = append(goalTitles, row.Goal)
		audit = append(audit, SourceRef{Kind: scoutTriggerTask, ID: row.ID})
	}

	var chatLines []string
	if args.Trigger == scoutTriggerChat && deps.Chat != nil && args.SourceID != "" {
		chatLines, err = deps.Chat.RecentLines(ctx, args.UserID, args.SourceID, scoutChatLines)
		if err != nil {
			return 0, audit, err
		}
		audit = append(audit, SourceRef{Kind: scoutTriggerChat, ID: args.SourceID})
	}
	if args.Trigger == scoutTriggerKnowledge && args.SourceID != "" {
		audit = append(audit, SourceRef{Kind: scoutTriggerKnowledge, ID: args.SourceID})
	}
	if args.Trigger == scoutTriggerTask && args.SourceID != "" {
		audit = append(audit, SourceRef{Kind: scoutTriggerTask, ID: args.SourceID})
	}

	proposer := deps.Propose(ctx, args.UserID)
	if proposer == nil {
		return 0, audit, fmt.Errorf("tasks: scout: proposer")
	}
	prompt := scoutPrompt(args.Trigger, args.SourceID, goalTitles, chatLines)
	ctx, cancel := context.WithTimeout(ctx, scoutProposeWait)
	defer cancel()

	raw, err := proposeTitlesTwice(ctx, proposer, prompt)
	if err != nil {
		return 0, audit, err
	}
	keptNotices, err := domain.Propose(raw, goalTitles)
	if err != nil {
		if errors.Is(err, domain.ErrScout) {
			return 0, audit, nil
		}
		return 0, audit, err
	}
	kept := 0
	for _, notice := range keptNotices {
		if _, err := deps.Notices.Insert(ctx, args.UserID, domain.NoticeProposal, notice.Title, nil); err != nil {
			return kept, audit, err
		}
		kept++
	}
	return kept, audit, nil
}

func scoutPrompt(trigger, sourceID string, goals, chatLines []string) string {
	var b strings.Builder
	b.WriteString("Propose new task titles only. Reply with a JSON array of strings, no markdown.\n")
	b.WriteString("Do not repeat an open task goal.\n")
	switch trigger {
	case scoutTriggerChat:
		b.WriteString("Trigger: chat message persisted.\n")
	case scoutTriggerTask:
		b.WriteString("Trigger: task reached Done or Failed.\n")
	case scoutTriggerKnowledge:
		b.WriteString("Trigger: knowledge source became ready.\n")
	}
	if sourceID != "" {
		b.WriteString("Source id: ")
		b.WriteString(sourceID)
		b.WriteByte('\n')
	}
	if len(goals) > 0 {
		b.WriteString("\nOpen task goals:\n")
		for _, goal := range goals {
			b.WriteString("- ")
			b.WriteString(goal)
			b.WriteByte('\n')
		}
	}
	if len(chatLines) > 0 {
		b.WriteString("\nRecent chat lines:\n")
		for _, line := range chatLines {
			b.WriteString("- ")
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	out := b.String()
	if len(out) > scoutPromptMax {
		return out[:scoutPromptMax]
	}
	return out
}

func proposeTitlesTwice(ctx context.Context, proposer Proposer, body string) ([]string, error) {
	raw, err := proposer.Propose(ctx, body)
	if err != nil {
		return nil, err
	}
	titles, err := parseTitleBatch(raw)
	if err == nil {
		return titles, nil
	}
	raw, err = proposer.Propose(ctx, body)
	if err != nil {
		return nil, err
	}
	titles, err = parseTitleBatch(raw)
	if err != nil {
		return nil, nil
	}
	return titles, nil
}

func parseTitleBatch(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	var titles []string
	if err := json.Unmarshal([]byte(raw), &titles); err != nil {
		return nil, err
	}
	return titles, nil
}

// IsRetry reports a transient gateway failure River should retry.
func IsRetry(err error) bool {
	return errors.Is(err, ErrRetry)
}
