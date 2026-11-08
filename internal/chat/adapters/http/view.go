package httpapi

import (
	"time"

	"github.com/upmo-ai/upmo/internal/chat/app"
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
	ID    string     `json:"id"`
	Lines []lineJSON `json:"lines"`
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
	body := chatJSON{ID: chat.ID, Lines: make([]lineJSON, 0, len(chat.Lines))}
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
	return lineJSON{
		ID:      line.ID,
		Role:    line.Role,
		Body:    line.Body,
		Model:   line.Model,
		Tokens:  line.Tokens,
		CostUSD: cost,
		At:      line.At.UTC().Format(time.RFC3339Nano),
	}
}
