package app

import (
	"strings"

	"github.com/upmo-ai/upmo/internal/platform/agentloop"
	"github.com/upmo-ai/upmo/internal/platform/trustfence"
)

const (
	SummaryTriggerPercent       = 60
	SummaryTargetPercent        = 30
	SummaryMaxOutputTokens      = 2048
	MinPriorExchangesForSummary = 2
	MaxFallbackExchangeDrops    = 8
)

type SummaryUpdate struct {
	Text                 string
	UpToLineID           string
	UpToSeq              int
	Tokens               int
	ReportedCostMicroUSD *int64
	ChargeMicroUSD       int64
}

// FormatSummaryPrompt fences stored summaries when the chat has read untrusted content.
func FormatSummaryPrompt(rollingSummary string, tainted bool) string {
	rollingSummary = strings.TrimSpace(rollingSummary)
	if rollingSummary == "" {
		return ""
	}
	if tainted {
		return trustfence.Wrap(trustfence.TagUntrust, "rolling_summary", rollingSummary)
	}
	return rollingSummary
}

// LinePromptText is the single place to add extracted attachment text when it is available.
func LinePromptText(line Line) string { return line.Body }

// LineTokens is the single place to count extracted attachment text when it is available.
func LineTokens(line Line) int { return agentloop.TokenEstimate(LinePromptText(line)) }

type exchange struct{ start, end int }

// CutForSummary keeps the newest complete exchanges and the current user line.
func CutForSummary(lines []Line, rollingSummary string, contextWindow int) (toSummarize, keep []Line, cutLineID string, cutSeq int, needed bool) {
	if contextWindow <= 0 || len(lines) < 5 {
		return nil, lines, "", 0, false
	}
	prior := lines[:len(lines)-1]
	var exchanges []exchange
	for i := 0; i < len(prior); {
		if prior[i].Role != "user" {
			i++
			continue
		}
		start, j := i, i+1
		for j < len(prior) && prior[j].Role == "assistant" {
			j++
		}
		if j > i+1 {
			exchanges = append(exchanges, exchange{start, j})
		}
		i = j
	}
	if len(exchanges) < MinPriorExchangesForSummary {
		return nil, lines, "", 0, false
	}
	total := agentloop.TokenEstimate(rollingSummary)
	for _, line := range lines {
		total += LineTokens(line)
	}
	if total <= contextWindow*SummaryTriggerPercent/100 {
		return nil, lines, "", 0, false
	}

	keepStart := len(exchanges) - 1
	keepTokens := LineTokens(lines[len(lines)-1])
	for _, line := range lines[exchanges[keepStart].start:exchanges[keepStart].end] {
		keepTokens += LineTokens(line)
	}
	target := contextWindow * SummaryTargetPercent / 100
	for keepStart > 0 {
		e := exchanges[keepStart-1]
		extra := 0
		for _, line := range lines[e.start:e.end] {
			extra += LineTokens(line)
		}
		if keepTokens+extra > target {
			break
		}
		keepStart--
		keepTokens += extra
	}
	if keepStart == 0 {
		return nil, lines, "", 0, false
	}
	split := exchanges[keepStart].start
	toSummarize, keep = lines[:split], lines[split:]
	last := toSummarize[len(toSummarize)-1]
	return toSummarize, keep, last.ID, last.Seq, true
}

// BuildSummarizerTranscript joins the previous summary and cut lines as untrusted data.
func BuildSummarizerTranscript(rollingSummary string, lines []Line) string {
	var b strings.Builder
	if s := strings.TrimSpace(rollingSummary); s != "" {
		b.WriteString("Prior summary:\n")
		b.WriteString(s)
		b.WriteString("\n\n")
	}
	for _, line := range lines {
		b.WriteString(line.Role)
		b.WriteString(": ")
		b.WriteString(LinePromptText(line))
		b.WriteString("\n\n")
	}
	return trustfence.Wrap(trustfence.TagUntrust, "transcript", strings.TrimSpace(b.String()))
}

// FallbackDropExchanges removes bounded oldest exchanges until messages fit.
func FallbackDropExchanges(lines []Line, rollingSummary string, tainted bool, contextWindow int, system string, tools []agentloop.ToolDef) ([]Line, error) {
	if contextWindow <= 0 {
		return lines, nil
	}
	budget := agentloop.PromptMessageBudget(contextWindow, system, tools)
	for dropped := 0; ; dropped++ {
		messages := summaryMessages(rollingSummary, tainted, lines)
		if estimateAgentMessages(messages) <= budget {
			return lines, nil
		}
		if dropped == MaxFallbackExchangeDrops {
			break
		}
		prior := lines[:max(0, len(lines)-1)]
		cut := -1
		for i, line := range prior {
			if line.Role == "user" {
				cut = i
				break
			}
		}
		if cut < 0 {
			break
		}
		end := cut + 1
		for end < len(prior) && prior[end].Role == "assistant" {
			end++
		}
		if end == cut+1 || end >= len(lines) {
			break
		}
		lines = append(lines[:cut], lines[end:]...)
	}
	return nil, &ModelError{Class: "context_exceeded"}
}

func summaryMessages(summary string, tainted bool, lines []Line) []agentloop.Message {
	out := make([]agentloop.Message, 0, len(lines)+2)
	if text := FormatSummaryPrompt(summary, tainted); text != "" {
		out = append(out, agentloop.Message{Role: agentloop.RoleUser, Text: "[ Prior chat summary ]\n" + text}, agentloop.Message{Role: agentloop.RoleAssistant, Text: "Understood."})
	}
	for _, line := range lines {
		text := LinePromptText(line)
		if n := len(out); n > 0 && out[n-1].Role == agentloop.MessageRole(line.Role) {
			out[n-1].Text += "\n\n" + text
		} else {
			out = append(out, agentloop.Message{Role: agentloop.MessageRole(line.Role), Text: text})
		}
	}
	return out
}

func estimateAgentMessages(messages []agentloop.Message) int {
	n := 0
	for _, message := range messages {
		n += agentloop.TokenEstimate(message.Text)
	}
	return n
}
