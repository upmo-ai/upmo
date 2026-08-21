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
	if toolsAt < 0 || systemAt < 0 || messagesAt < 0 || !(toolsAt < systemAt && systemAt < messagesAt) {
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
	if len(req.Tools) != 1 || req.Tools[0].Name != "search" {
		t.Fatalf("tools = %+v", req.Tools)
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
	if len(req.Tools) != 1 || req.Tools[0].Name != retrieveOutput {
		t.Fatalf("tools = %+v", req.Tools)
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
