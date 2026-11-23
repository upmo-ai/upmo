package httpapi

import (
	"time"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
)

// summaryJSON is one chat in a list, and the answer to opening one.
type summaryJSON struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Preview      string `json:"preview"`
	LastActivity string `json:"last_activity"`
}

// chatJSON is one chat in full.
type chatJSON struct {
	ID          string     `json:"id"`
	Lines       []lineJSON `json:"lines"`
	Tainted     bool       `json:"tainted"`
	PrivateRead bool       `json:"private_read"`
}

// lineJSON is one line of a chat in full.
type lineJSON struct {
	ID      string   `json:"id"`
	Role    string   `json:"role"`
	Body    string   `json:"body"`
	Model   *string  `json:"model"`
	Tokens  int      `json:"tokens"`
	CostUSD *float64 `json:"cost_usd"`
	At      string   `json:"at"`
	// Status is done, failed, or stopped. ReplyTo is the user line a reply answers.
	// Attempts, on a failed line, are the models tried and why each failed.
	Status   string        `json:"status"`
	ReplyTo  *string       `json:"reply_to"`
	Attempts []attemptJSON `json:"attempts"`
	// Route, Pin and AgentID, on a user line, are the picks it was sent with;
	// each is null when the person made no pick.
	Route   *string  `json:"route"`
	Pin     *pinJSON `json:"pin"`
	AgentID *string  `json:"agent_id"`
	// Pending, on a user line, says no reply has been stored for it yet.
	Pending bool `json:"pending"`
}

// pinJSON is the model a user line was pinned to. The account is left out
// for anyone but the line's author.
type pinJSON struct {
	AccountID string `json:"account_id,omitempty"`
	Model     string `json:"model"`
}

// attemptJSON is one model a reply tried and the phrase for why it failed.
type attemptJSON struct {
	Model  string `json:"model"`
	Reason string `json:"reason"`
}

func summaryBody(summary app.ChatSummary) summaryJSON {
	return summaryJSON{
		ID:           summary.ID,
		Title:        summary.Title,
		Preview:      summary.Preview,
		LastActivity: summary.LastActivity.UTC().Format(time.RFC3339Nano),
	}
}

func chatBody(chat app.Chat) chatJSON {
	body := chatJSON{ID: chat.ID, Lines: make([]lineJSON, 0, len(chat.Lines)), Tainted: chat.Tainted, PrivateRead: chat.PrivateRead}
	for _, line := range chat.Lines {
		body.Lines = append(body.Lines, lineBody(line))
	}
	return body
}

// lineBody shapes one line for the wire. Cost is micro-dollars in the store
// and dollars here; unknown stays null rather than zero.
func lineBody(line app.Line) lineJSON {
	var cost *float64
	if line.CostMicroUSD != nil {
		usd := float64(*line.CostMicroUSD) / 1e6
		cost = &usd
	}
	attempts := make([]attemptJSON, 0, len(line.Attempts))
	for _, attempt := range line.Attempts {
		attempts = append(attempts, attemptJSON{Model: attempt.Model, Reason: domain.Reason(attempt.Class)})
	}
	var pin *pinJSON
	if line.PinModel != nil {
		pin = &pinJSON{Model: *line.PinModel}
		if line.PinAccount != nil {
			pin.AccountID = *line.PinAccount
		}
	}
	return lineJSON{
		ID:       line.ID,
		Role:     line.Role,
		Body:     line.Body,
		Model:    line.Model,
		Tokens:   line.Tokens,
		CostUSD:  cost,
		At:       line.At.UTC().Format(time.RFC3339Nano),
		Status:   line.Status,
		ReplyTo:  line.ReplyTo,
		Attempts: attempts,
		Route:    line.Route,
		Pin:      pin,
		AgentID:  line.AgentID,
		Pending:  line.Pending,
	}
}
