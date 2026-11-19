package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildOrderIsToolsSystemMessages(t *testing.T) {
	req, err := Build(Input{
		Tools:    []Tool{{Name: "search", Schema: json.RawMessage(`{"type":"object","properties":{}}`)}},
		System:   "be brief",
		Messages: []Message{{Role: RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	toolsAt := strings.Index(text, `"tools"`)
	systemAt := strings.Index(text, `"system"`)
	messagesAt := strings.Index(text, `"messages"`)
	if toolsAt < 0 || systemAt < 0 || messagesAt < 0 || toolsAt >= systemAt || systemAt >= messagesAt {
		t.Fatalf("order = %s", text)
	}
	if len(req.Cache) != 2 || req.Cache[0] != BreakTools || req.Cache[1] != BreakSystem {
		t.Fatalf("cache = %v", req.Cache)
	}
}

func TestBuildRejectsSystemRoleInMessages(t *testing.T) {
	_, err := Build(Input{Messages: []Message{{Role: Role("system"), Text: "no"}}})
	if err == nil {
		t.Fatal("system in messages")
	}
}

func TestMaskEmptyAllowKeepsCatalogUnlessMasked(t *testing.T) {
	req, err := Build(Input{
		Tools:    []Tool{{Name: "search"}, {Name: "write"}},
		Mask:     true,
		Messages: []Message{{Role: RoleUser, Text: "q"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Tools) != 0 {
		t.Fatalf("tools = %+v", req.Tools)
	}
}

func TestMaskTools(t *testing.T) {
	req, err := Build(Input{
		Tools: []Tool{
			{Name: "search"},
			{Name: "write"},
		},
		AllowTools: []string{"search"},
		Messages:   []Message{{Role: RoleUser, Text: "q"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{retrieveOutput, findTool, "search"}
	if len(req.Tools) != len(want) {
		t.Fatalf("tools = %+v", req.Tools)
	}
	for i, name := range want {
		if req.Tools[i].Name != name {
			t.Fatalf("tools[%d] = %q, want %q", i, req.Tools[i].Name, name)
		}
	}
}

func TestCanonicalSchemaSortsKeys(t *testing.T) {
	got, err := CanonicalSchema(json.RawMessage(`{"b":1,"a":{"d":true,"c":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":{"c":false,"d":true},"b":1}` {
		t.Fatalf("schema = %s", got)
	}
}

func TestOffloadLargeToolResults(t *testing.T) {
	small := "ok"
	big := strings.Repeat("x", 40)
	req, err := Build(Input{
		OffloadAt: 5,
		Messages: []Message{
			{Role: RoleUser, Text: "go"},
			{Role: RoleTool, ToolID: "1", Text: small},
			{Role: RoleTool, ToolID: "2", Text: big},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.Messages[1].Offload || req.Messages[1].Text != small {
		t.Fatalf("small tool = %+v", req.Messages[1])
	}
	if !req.Messages[2].Offload || req.Messages[2].Text != retrieveOutput || req.Messages[2].ToolID != "2" {
		t.Fatalf("large tool = %+v", req.Messages[2])
	}
	if len(req.Tools) != 2 || req.Tools[0].Name != retrieveOutput || req.Tools[1].Name != findTool {
		t.Fatalf("tools = %+v", req.Tools)
	}
}

func TestStaticCorePrefixUnchangedAcrossOffload(t *testing.T) {
	tools := []Tool{
		{Name: retrieveOutput, Description: "custom", Schema: json.RawMessage(`{}`)},
		{Name: "search", Description: "search docs", Schema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)},
	}
	turn0, err := Build(Input{
		Tools:    tools,
		System:   "be brief",
		Messages: []Message{{Role: RoleUser, Text: "find go docs"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	turnN, err := Build(Input{
		Tools:     tools,
		System:    "be brief",
		OffloadAt: 5,
		Messages: []Message{
			{Role: RoleUser, Text: "find go docs"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "search", Arguments: json.RawMessage(`{"q":"go"}`)}}},
			{Role: RoleTool, ToolID: "c1", ToolName: "search", Text: strings.Repeat("x", 100)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t0Tools, err := json.Marshal(turn0.Tools)
	if err != nil {
		t.Fatal(err)
	}
	tnTools, err := json.Marshal(turnN.Tools)
	if err != nil {
		t.Fatal(err)
	}
	if string(t0Tools) != string(tnTools) {
		t.Fatalf("tools changed on offload:\nturn0: %s\nturnN: %s", t0Tools, tnTools)
	}
	t0Cache, _ := json.Marshal(turn0.Cache)
	tnCache, _ := json.Marshal(turnN.Cache)
	if string(t0Cache) != string(tnCache) {
		t.Fatalf("cache breakpoints changed on offload:\nturn0: %s\nturnN: %s", t0Cache, tnCache)
	}
	if len(turn0.Tools) != 3 || turn0.Tools[0].Name != retrieveOutput || turn0.Tools[1].Name != findTool || turn0.Tools[2].Name != "search" {
		t.Fatalf("unexpected tool prefix: %+v", turn0.Tools)
	}
	if !turnN.Messages[2].Offload {
		t.Fatalf("expected turnN tool result to be offloaded: %+v", turnN.Messages[2])
	}
}

func TestCoreToolsOptionWithoutCustomTools(t *testing.T) {
	turn0, err := Build(Input{
		CoreTools: true,
		Messages:  []Message{{Role: RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	turnN, err := Build(Input{
		CoreTools: true,
		OffloadAt: 5,
		Messages: []Message{
			{Role: RoleUser, Text: "hi"},
			{Role: RoleTool, ToolID: "c1", Text: strings.Repeat("y", 60), IsError: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	b0, _ := json.Marshal(turn0.Tools)
	bn, _ := json.Marshal(turnN.Tools)
	if string(b0) != string(bn) {
		t.Fatalf("core tools changed: %s vs %s", b0, bn)
	}
	if !turnN.Messages[1].IsError || !turnN.Messages[1].Offload {
		t.Fatalf("expected IsError and Offload preserved: %+v", turnN.Messages[1])
	}
}

func TestToolChoiceNormalizationAndOrder(t *testing.T) {
	var parsed ToolChoice
	if err := json.Unmarshal([]byte(`"any"`), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Mode != ToolChoiceAny || parsed.Name != "" {
		t.Fatalf("string tool_choice = %+v", parsed)
	}
	if err := json.Unmarshal([]byte(`{"mode":"tool","name":"search"}`), &parsed); err != nil {
		t.Fatal(err)
	}
	req, err := Build(Input{
		Tools:           []Tool{{Name: "search"}},
		System:          "s",
		Messages:        []Message{{Role: RoleUser, Text: "m"}},
		ToolChoice:      parsed,
		MaxOutputTokens: 128,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	cacheAt := strings.Index(text, `"cache"`)
	choiceAt := strings.Index(text, `"tool_choice"`)
	if cacheAt < 0 || choiceAt < 0 || choiceAt <= cacheAt {
		t.Fatalf("tool_choice must sit after prefix/cache: %s", text)
	}
	if _, err := Build(Input{
		Messages:   []Message{{Role: RoleUser, Text: "m"}},
		ToolChoice: ToolChoice{Mode: "bogus"},
	}); err == nil {
		t.Fatal("expected error for invalid tool_choice mode")
	}
	if _, err := Build(Input{
		Messages:   []Message{{Role: RoleUser, Text: "m"}},
		ToolChoice: ToolChoice{Mode: ToolChoiceAuto, Name: "search"},
	}); err == nil {
		t.Fatal("expected error when auto carries a tool name")
	}
	if _, err := Build(Input{
		Messages:   []Message{{Role: RoleUser, Text: "m"}},
		ToolChoice: ToolChoice{Mode: ToolChoiceTool},
	}); err == nil {
		t.Fatal("expected error when tool mode misses tool name")
	}
}

func TestRequestJSONHasOnlyPrefixThenMessages(t *testing.T) {
	req, err := Build(Input{
		Tools:    []Tool{{Name: "t"}},
		System:   "s",
		Messages: []Message{{Role: RoleUser, Text: "m"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	for k := range obj {
		switch k {
		case "tools", "system", "messages", "cache":
		default:
			t.Fatalf("extra field %s in %s", k, raw)
		}
	}
}

func TestRequestPartsAndUnsupportedInputError(t *testing.T) {
	req, err := Build(Input{
		Messages: []Message{{
			Role: RoleUser,
			Parts: []Part{
				{Kind: " TEXT ", Text: "look"},
				{Kind: "Image", MIMEType: "image/png", Data: "abc"},
				{Kind: "file", Name: "doc.pdf"},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := req.RequiredInputs(); len(got) != 2 || got[0] != InputImage || got[1] != InputFile {
		t.Fatalf("RequiredInputs = %v", got)
	}
	if _, err := Build(Input{
		Messages: []Message{{Role: RoleUser, Parts: []Part{{Kind: "video"}}}},
	}); err == nil {
		t.Fatal("expected error for unknown part kind")
	}
	withSuggestion := UnsupportedInputError{Model: "gpt-3.5-turbo", Input: InputImage, Suggestion: "gpt-4o"}
	if got := withSuggestion.Error(); !strings.Contains(got, "gpt-3.5-turbo") || !strings.Contains(got, "image") || !strings.Contains(got, "gpt-4o") {
		t.Fatalf("UnsupportedInputError = %q", got)
	}
	noSuggestion := UnsupportedInputError{Model: "gpt-3.5-turbo", Input: InputAudio}
	if got := noSuggestion.Error(); !strings.Contains(got, "gpt-3.5-turbo") || !strings.Contains(got, "audio") {
		t.Fatalf("UnsupportedInputError = %q", got)
	}
}
