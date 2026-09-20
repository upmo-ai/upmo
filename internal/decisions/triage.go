package decisions

import (
	"context"
	"strings"
	"unicode/utf8"
)

const (
	// RouteTriage is the decide route that chooses how to handle a channel message.
	RouteTriage = "channel-triage"
	// TriageReply is the choice to reply to a message.
	TriageReply = "reply"
	// TriageTask is the choice to create a task from a message.
	TriageTask = "task"
	// TriageIgnore is the choice to ignore a message.
	TriageIgnore = "ignore"
)

// Message is one channel message and its expected triage choice.
type Message struct {
	Body  string
	Label string
}

// Triage accepts a channel-triage config when its labelled accuracy meets the bar.
func Triage(ctx context.Context, book Gates, access Access, hops []Hop, bar int, messages []Message, run Runner) error {
	if len(messages) == 0 {
		return ErrConfig
	}
	examples := make([]Example, len(messages))
	for i, message := range messages {
		body := strings.TrimSpace(message.Body)
		if runes := utf8.RuneCountInString(body); runes < 1 || runes > 4000 {
			return ErrConfig
		}
		label := strings.TrimSpace(message.Label)
		if label != TriageReply && label != TriageTask && label != TriageIgnore {
			return ErrConfig
		}
		examples[i] = Example{
			Prompt:  body,
			Options: []string{TriageReply, TriageTask, TriageIgnore},
			Choice:  label,
		}
	}
	return Accept(ctx, book, access, Config{
		Name: RouteTriage,
		Kind: KindChoice,
		Hops: hops,
	}, bar, examples, run)
}
