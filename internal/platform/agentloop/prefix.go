package agentloop

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

const (
	// LessonIndexCap is the maximum number of non-inline house-rule cards in BreakSystem.
	LessonIndexCap = 24
	// DefaultOffloadTokens is the token estimate threshold above which tool output is offloaded.
	DefaultOffloadTokens = 2000

	toolRetrieveOutput = "retrieve_output"
	toolFindTool       = "find_tool"
)

// Lesson is one full house rule.
type Lesson struct {
	ID           string
	Title        string
	Cue          string
	Body         string
	AlwaysInline bool
}

// LessonCard is a lightweight [Title -> Cue] index entry for non-inline rules.
type LessonCard struct {
	ID    string
	Title string
	Cue   string
}

// OutputStore persists and retrieves offloaded tool outputs and findings.
type OutputStore interface {
	SaveOutput(ctx context.Context, run *RunState, title, body string, untrusted bool) (string, error)
	RetrieveOutput(ctx context.Context, run *RunState, id string) (string, bool, error)
}

// CoreTools returns the static turn-0 core tools that always lead BreakTools:
// retrieve_output and find_tool.
func CoreTools() []ToolDef {
	return []ToolDef{
		{
			Name:        toolRetrieveOutput,
			Description: "fetch an offloaded tool result",
			Schema:      json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
			Category:    CategoryDirect,
			Op:          OpRead,
		},
		{
			Name:        toolFindTool,
			Description: "search available tools by name or description",
			Schema:      json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
			Category:    CategoryDirect,
			Op:          OpRead,
		},
	}
}

// AssembleSystemPrefix builds the stable BreakSystem prompt prefix:
// standing instructions, agent header + instructions + always_inline house
// rules, and a lightweight [Title -> Cue] index of non-inline rules.
func AssembleSystemPrefix(
	standingInstructions string,
	agentName string,
	agentInstructions string,
	inlineLessons []Lesson,
	indexCards []LessonCard,
) string {
	var sections []string
	if s := strings.TrimSpace(standingInstructions); s != "" {
		sections = append(sections, s)
	}

	var agentParts []string
	if inst := strings.TrimSpace(agentInstructions); inst != "" {
		agentParts = append(agentParts, inst)
	}
	for _, l := range inlineLessons {
		if !l.AlwaysInline || strings.TrimSpace(l.Body) == "" {
			continue
		}
		agentParts = append(agentParts, "## "+strings.TrimSpace(l.Title)+"\n\n"+strings.TrimSpace(l.Body))
	}
	if len(agentParts) > 0 {
		overlay := strings.Join(agentParts, "\n\n")
		if name := strings.TrimSpace(agentName); name != "" {
			sections = append(sections, "# Agent: "+name+"\n\n"+overlay)
		} else {
			sections = append(sections, overlay)
		}
	}

	if len(indexCards) > 0 {
		sorted := make([]LessonCard, len(indexCards))
		copy(sorted, indexCards)
		sort.Slice(sorted, func(i, j int) bool {
			ti := strings.TrimSpace(sorted[i].Title)
			tj := strings.TrimSpace(sorted[j].Title)
			if ti != tj {
				return ti < tj
			}
			if sorted[i].ID != sorted[j].ID {
				return sorted[i].ID < sorted[j].ID
			}
			return strings.TrimSpace(sorted[i].Cue) < strings.TrimSpace(sorted[j].Cue)
		})
		limit := min(len(sorted), LessonIndexCap)
		var b strings.Builder
		b.WriteString("## House rules index\n")
		for _, card := range sorted[:limit] {
			title := strings.TrimSpace(card.Title)
			cue := strings.TrimSpace(card.Cue)
			if title == "" && cue == "" {
				continue
			}
			if card.ID != "" {
				fmt.Fprintf(&b, "\n- [%s -> %s] (id: %s)", title, cue, card.ID)
			} else {
				fmt.Fprintf(&b, "\n- [%s -> %s]", title, cue)
			}
		}
		if b.Len() > len("## House rules index\n") {
			sections = append(sections, b.String())
		}
	}

	return strings.Join(sections, "\n\n")
}

// ToolCatalog maintains an append-only active tool list for prompt-cache
// stability, starting with retrieve_output and find_tool from turn 0.
type ToolCatalog struct {
	mu           sync.RWMutex
	active       []ToolDef
	activeByName map[string]ToolDef
	discoverable []ToolDef
}

// NewToolCatalog constructs a catalog with CoreTools first, followed by sorted
// initial tools, plus a pool of discoverable tools searchable by find_tool.
func NewToolCatalog(initial, discoverable []ToolDef) *ToolCatalog {
	c := &ToolCatalog{
		activeByName: make(map[string]ToolDef),
	}
	for _, core := range CoreTools() {
		c.active = append(c.active, core)
		c.activeByName[core.Name] = core
	}
	sortedInit := make([]ToolDef, 0, len(initial))
	for _, t := range initial {
		if t.Name == "" || t.Name == toolRetrieveOutput || t.Name == toolFindTool {
			continue
		}
		sortedInit = append(sortedInit, t)
	}
	sort.Slice(sortedInit, func(i, j int) bool { return sortedInit[i].Name < sortedInit[j].Name })
	c.Append(sortedInit...)

	c.discoverable = make([]ToolDef, len(discoverable))
	copy(c.discoverable, discoverable)
	return c
}

// PromptTools returns a snapshot of active tools in append-only prefix order.
func (c *ToolCatalog) PromptTools() []ToolDef {
	if c == nil {
		return CoreTools()
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]ToolDef, len(c.active))
	copy(out, c.active)
	return out
}

// Append adds tools to the end of the active list if not already present,
// preserving existing prefix order for prompt caching.
func (c *ToolCatalog) Append(tools ...ToolDef) []ToolDef {
	if c == nil || len(tools) == 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var added []ToolDef
	for _, t := range tools {
		if t.Name == "" {
			continue
		}
		if _, exists := c.activeByName[t.Name]; exists {
			continue
		}
		c.active = append(c.active, t)
		c.activeByName[t.Name] = t
		added = append(added, t)
	}
	return added
}

// Get looks up an active tool definition by name.
func (c *ToolCatalog) Get(name string) (ToolDef, bool) {
	if c == nil {
		return ToolDef{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.activeByName[name]
	return t, ok
}

// Find searches discoverable tools by name or description and appends any
// newly matched tools to the end of the active tool list.
func (c *ToolCatalog) Find(query string) []ToolDef {
	if c == nil {
		return nil
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	c.mu.RLock()
	var matches []ToolDef
	for _, t := range c.discoverable {
		if strings.Contains(strings.ToLower(t.Name), q) ||
			strings.Contains(strings.ToLower(t.Description), q) {
			matches = append(matches, t)
		}
	}
	c.mu.RUnlock()
	c.Append(matches...)
	return matches
}

// TokenEstimate estimates token count using a 4-bytes-per-token heuristic.
func TokenEstimate(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

// MaybeOffload stores a large tool output via store when its token estimate
// exceeds offloadAt and returns the compact sentinel reference.
func MaybeOffload(
	ctx context.Context,
	store OutputStore,
	run *RunState,
	toolName string,
	output string,
	untrusted bool,
	offloadAt int,
) (text string, offloaded bool, err error) {
	if offloadAt <= 0 {
		offloadAt = DefaultOffloadTokens
	}
	if store == nil || TokenEstimate(output) <= offloadAt {
		return output, false, nil
	}
	id, err := store.SaveOutput(ctx, run, toolName, output, untrusted)
	if err != nil {
		return "", false, err
	}
	return fmt.Sprintf("%s:%s", toolRetrieveOutput, id), true, nil
}

// BuildPromptRequest constructs a PromptRequest with BreakTools and BreakSystem
// cache marks and TeamID propagation for single-team gateway narrowing.
func BuildPromptRequest(run *RunState, system string, messages []Message) PromptRequest {
	tools := CoreTools()
	if run != nil && run.Catalog != nil {
		tools = run.Catalog.PromptTools()
	}
	copiedMsgs := make([]Message, len(messages))
	copy(copiedMsgs, messages)

	var cache []Breakpoint
	if len(tools) > 0 {
		cache = append(cache, BreakTools)
	}
	if strings.TrimSpace(system) != "" {
		cache = append(cache, BreakSystem)
	}

	req := PromptRequest{
		Tools:    tools,
		System:   system,
		Messages: copiedMsgs,
		Cache:    cache,
	}
	if run != nil {
		narrowed := NarrowRLS(run.Access)
		req.UserID = run.UserID
		req.AgentID = run.AgentID
		req.RunID = run.RunID
		req.TeamID = narrowed.GatewayTeamID
	}
	return req
}
