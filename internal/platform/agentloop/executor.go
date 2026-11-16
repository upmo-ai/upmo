package agentloop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// SandboxCommand describes a command executed inside a sandbox.
type SandboxCommand struct {
	Args    []string
	Dir     string
	Timeout time.Duration
}

// SandboxOutput holds command output and exit status from a sandbox.
type SandboxOutput struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// SandboxManager is the port for lazy sandboxd seat provisioning and execution.
type SandboxManager interface {
	EnsureSeat(ctx context.Context, run *RunState) (seatID string, err error)
	Exec(ctx context.Context, seatID string, cmd SandboxCommand) (SandboxOutput, error)
	ReadFile(ctx context.Context, seatID, path string) ([]byte, error)
	WriteFile(ctx context.Context, seatID, path string, data []byte) error
}

// KnowledgeHit is one knowledge search chunk with provenance.
type KnowledgeHit struct {
	SourceID  string
	Seq       int
	Body      string
	Untrusted bool
}

// KnowledgeSearcher searches curated/web knowledge folders for a run.
type KnowledgeSearcher interface {
	Search(ctx context.Context, run *RunState, folderID, query string) ([]KnowledgeHit, error)
}

// LessonOpener opens one non-inline house rule on demand.
type LessonOpener interface {
	OpenLesson(ctx context.Context, agentID, lessonID string) (Lesson, error)
}

// MemorySearcher searches confirmed user memory facts.
type MemorySearcher interface {
	SearchMemory(ctx context.Context, userID, query string) (text string, untrusted bool, private bool, err error)
}

// ToolHandler executes one direct in-process tool call.
type ToolHandler func(ctx context.Context, run *RunState, call ToolCall) (ToolResult, error)

// Executor dispatches direct in-process tools and lazy sandbox tools.
type Executor interface {
	Execute(ctx context.Context, run *RunState, call ToolCall) (ToolResult, error)
}

// IsSandboxTool reports whether toolName requires a sandbox seat.
func IsSandboxTool(toolName string) bool {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "shell", "read_file", "write_file", "command", "repo":
		return true
	default:
		return false
	}
}

// DefaultExecutor routes direct in-process tools without touching sandboxd,
// and lazily provisions a sandboxd seat ONLY when a sandbox tool is invoked.
type DefaultExecutor struct {
	Sandbox   SandboxManager
	Knowledge KnowledgeSearcher
	Lessons   LessonOpener
	Memory    MemorySearcher
	Outputs   OutputStore
	HTTP      *HTTPConnectorClient
	Direct    map[string]ToolHandler
}

// DoConnectorHTTP executes an outbound connector HTTP request via e.HTTP,
// forwarding call.IdempotencyKey as the Idempotency-Key header.
func (e DefaultExecutor) DoConnectorHTTP(
	ctx context.Context,
	call ToolCall,
	method string,
	targetURL string,
	allowedHost string,
	body io.Reader,
	credentialRef string,
	headers map[string]string,
) (status int, respBody []byte, digest string, err error) {
	if e.HTTP == nil {
		return 0, nil, "", fmt.Errorf("agentloop: connector http client nil")
	}
	return e.HTTP.Do(ctx, method, targetURL, allowedHost, body, credentialRef, call.IdempotencyKey, headers)
}

// Execute dispatches call to a direct handler or to the sandbox manager.
func (e DefaultExecutor) Execute(ctx context.Context, run *RunState, call ToolCall) (ToolResult, error) {
	if handler, ok := e.Direct[call.Name]; ok && handler != nil {
		return handler(ctx, run, call)
	}
	switch call.Name {
	case toolFindTool:
		return e.execFindTool(run, call)
	case toolRetrieveOutput:
		return e.execRetrieveOutput(ctx, run, call)
	case "save_output":
		return e.execSaveOutput(ctx, run, call)
	case "open_lesson":
		return e.execOpenLesson(ctx, run, call)
	case "search_knowledge":
		return e.execSearchKnowledge(ctx, run, call)
	case "search_memory":
		return e.execSearchMemory(ctx, run, call)
	}

	def, hasDef := run.Catalog.Get(call.Name)
	if IsSandboxTool(call.Name) || (hasDef && def.Category == CategorySandbox) {
		return e.execSandbox(ctx, run, call)
	}
	return ToolResult{Output: fmt.Sprintf("unknown tool %q", call.Name), IsError: true}, fmt.Errorf("agentloop: unknown tool %q", call.Name)
}

func (e DefaultExecutor) execFindTool(run *RunState, call ToolCall) (ToolResult, error) {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return ToolResult{Output: "invalid arguments", IsError: true}, err
	}
	found := run.Catalog.Find(args.Query)
	raw, err := json.Marshal(found)
	if err != nil {
		return ToolResult{IsError: true}, err
	}
	return ToolResult{Output: string(raw), DiscoveredTools: found}, nil
}

func (e DefaultExecutor) execRetrieveOutput(ctx context.Context, run *RunState, call ToolCall) (ToolResult, error) {
	if e.Outputs == nil {
		return ToolResult{Output: "output store unavailable", IsError: true}, fmt.Errorf("agentloop: output store nil")
	}
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return ToolResult{Output: "invalid arguments", IsError: true}, err
	}
	body, untrusted, err := e.Outputs.RetrieveOutput(ctx, run, args.ID)
	if err != nil {
		return ToolResult{Output: err.Error(), IsError: true}, err
	}
	return ToolResult{Output: body, Untrusted: untrusted}, nil
}

func (e DefaultExecutor) execSaveOutput(ctx context.Context, run *RunState, call ToolCall) (ToolResult, error) {
	if e.Outputs == nil {
		return ToolResult{Output: "output store unavailable", IsError: true}, fmt.Errorf("agentloop: output store nil")
	}
	var args struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return ToolResult{Output: "invalid arguments", IsError: true}, err
	}
	id, err := e.Outputs.SaveOutput(ctx, run, args.Title, args.Body, run != nil && run.Exposure.Tainted)
	if err != nil {
		return ToolResult{Output: err.Error(), IsError: true}, err
	}
	return ToolResult{Output: fmt.Sprintf(`{"id":%q}`, id)}, nil
}

func (e DefaultExecutor) execOpenLesson(ctx context.Context, run *RunState, call ToolCall) (ToolResult, error) {
	if e.Lessons == nil {
		return ToolResult{Output: "lesson opener unavailable", IsError: true}, fmt.Errorf("agentloop: lessons nil")
	}
	var args struct {
		LessonID string `json:"lesson_id"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return ToolResult{Output: "invalid arguments", IsError: true}, err
	}
	agentID := ""
	if run != nil {
		agentID = run.AgentID
	}
	lesson, err := e.Lessons.OpenLesson(ctx, agentID, args.LessonID)
	if err != nil {
		return ToolResult{Output: err.Error(), IsError: true}, err
	}
	raw, err := json.Marshal(lesson)
	if err != nil {
		return ToolResult{IsError: true}, err
	}
	return ToolResult{Output: string(raw)}, nil
}

func (e DefaultExecutor) execSearchKnowledge(ctx context.Context, run *RunState, call ToolCall) (ToolResult, error) {
	if e.Knowledge == nil {
		return ToolResult{Output: "knowledge search unavailable", IsError: true}, fmt.Errorf("agentloop: knowledge nil")
	}
	var args struct {
		FolderID string `json:"folder_id"`
		Query    string `json:"query"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return ToolResult{Output: "invalid arguments", IsError: true}, err
	}
	hits, err := e.Knowledge.Search(ctx, run, args.FolderID, args.Query)
	if err != nil {
		return ToolResult{Output: err.Error(), IsError: true}, err
	}
	untrusted := false
	for _, hit := range hits {
		if hit.Untrusted {
			untrusted = true
			break
		}
	}
	raw, err := json.Marshal(hits)
	if err != nil {
		return ToolResult{IsError: true}, err
	}
	return ToolResult{Output: string(raw), Untrusted: untrusted, Private: true}, nil
}

func (e DefaultExecutor) execSearchMemory(ctx context.Context, run *RunState, call ToolCall) (ToolResult, error) {
	if e.Memory == nil {
		return ToolResult{Output: "memory search unavailable", IsError: true}, fmt.Errorf("agentloop: memory nil")
	}
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return ToolResult{Output: "invalid arguments", IsError: true}, err
	}
	userID := ""
	if run != nil {
		userID = run.UserID
	}
	text, untrusted, private, err := e.Memory.SearchMemory(ctx, userID, args.Query)
	if err != nil {
		return ToolResult{Output: err.Error(), IsError: true}, err
	}
	return ToolResult{Output: text, Untrusted: untrusted, Private: private}, nil
}

func (e DefaultExecutor) execSandbox(ctx context.Context, run *RunState, call ToolCall) (ToolResult, error) {
	if e.Sandbox == nil {
		return ToolResult{Output: "sandbox unavailable", IsError: true}, fmt.Errorf("agentloop: sandbox manager nil")
	}
	if run.SeatID == "" {
		seatID, err := e.Sandbox.EnsureSeat(ctx, run)
		if err != nil {
			return ToolResult{Output: err.Error(), IsError: true}, err
		}
		run.SeatID = seatID
	}
	switch call.Name {
	case "read_file":
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return ToolResult{Output: "invalid arguments", IsError: true}, err
		}
		data, err := e.Sandbox.ReadFile(ctx, run.SeatID, args.Path)
		if err != nil {
			return ToolResult{Output: err.Error(), IsError: true}, err
		}
		return ToolResult{Output: string(data)}, nil
	case "write_file":
		var args struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return ToolResult{Output: "invalid arguments", IsError: true}, err
		}
		if err := e.Sandbox.WriteFile(ctx, run.SeatID, args.Path, []byte(args.Content)); err != nil {
			return ToolResult{Output: err.Error(), IsError: true}, err
		}
		return ToolResult{Output: "ok"}, nil
	default: // shell, command, repo
		var args struct {
			Args    []string `json:"args"`
			Command string   `json:"command"`
			Dir     string   `json:"dir"`
			Timeout int      `json:"timeout_seconds"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return ToolResult{Output: "invalid arguments", IsError: true}, err
		}
		cmdArgs := args.Args
		if len(cmdArgs) == 0 && strings.TrimSpace(args.Command) != "" {
			cmdArgs = []string{"sh", "-c", args.Command}
		}
		var timeout time.Duration
		if args.Timeout > 0 {
			timeout = time.Duration(args.Timeout) * time.Second
		}
		out, err := e.Sandbox.Exec(ctx, run.SeatID, SandboxCommand{
			Args:    cmdArgs,
			Dir:     args.Dir,
			Timeout: timeout,
		})
		if err != nil {
			return ToolResult{Output: err.Error(), IsError: true}, err
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return ToolResult{IsError: true}, err
		}
		return ToolResult{Output: string(raw)}, nil
	}
}
