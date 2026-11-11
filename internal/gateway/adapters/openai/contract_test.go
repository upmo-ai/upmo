package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// The contract tests replay one saved stream per vendor that the compatible
// adapter serves (testdata/contract/<vendor>.sse). They run the adapter at the
// vendor's real base URL, so the URL it builds is the one it would dial, and
// check what it sends, the text deltas it hands on, and the usage it ends
// with. Nothing leaves the process: a transport hands every request to a local
// test server.
//
// The saved streams were written from each vendor's documented stream format,
// not recorded from a live call. Replacing one with a real capture is a file
// swap; the expectations below are the only other thing to update.

const contractDir = "testdata/contract"

// contractUsage is the usage each saved stream must end with. Every stream
// carries the text "Hel" then "lo".
var contractUsage = map[string]domain.Usage{
	"openai":     {Arrived: true, Input: 12, Output: 5, CacheRead: 8},
	"gemini":     {Arrived: true, Input: 9, Output: 4},
	"nebius":     {Arrived: true, Input: 15, Output: 6},
	"ollama":     {Arrived: true, Input: 21, Output: 7},
	"deepseek":   {Arrived: true, Input: 11, Output: 5, CacheRead: 3},
	"groq":       {Arrived: true, Input: 14, Output: 5},
	"xai":        {Arrived: true, Input: 13, Output: 5},
	"openrouter": {Arrived: true, Input: 10, Output: 5, CacheRead: 2},
	"mistral":    {Arrived: true, Input: 18, Output: 6},
	"together":   {Arrived: true, Input: 16, Output: 5},
	"cerebras":   {Arrived: true, Input: 17, Output: 5},
	"fireworks":  {Arrived: true, Input: 19, Output: 5},
	"perplexity": {Arrived: true, Input: 20, Output: 5},
	"cohere":     {Arrived: true, Input: 22, Output: 6},
	"lmstudio":   {Arrived: true, Input: 8, Output: 5},
}

// redirect hands every request to one test server and keeps the URL each was
// addressed to.
type redirect struct {
	to    *url.URL
	mu    sync.Mutex
	asked []string
}

func (r *redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.asked = append(r.asked, req.URL.String())
	r.mu.Unlock()
	moved := req.Clone(req.Context())
	moved.URL.Scheme, moved.URL.Host = r.to.Scheme, r.to.Host
	moved.Host = ""
	return http.DefaultTransport.RoundTrip(moved)
}

func (r *redirect) lastAsked() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.asked) == 0 {
		return ""
	}
	return r.asked[len(r.asked)-1]
}

// contractCall is what the test server saw in one request.
type contractCall struct {
	Method, Auth, Type string
	Body               map[string]any
}

// contractServer answers like a compatible vendor: the saved stream for a
// streamed chat call, and a small fixed answer for everything else.
type contractServer struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls []contractCall
}

func newContractServer(t *testing.T, stream []byte) *contractServer {
	t.Helper()
	cs := &contractServer{}
	cs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := contractCall{Method: r.Method, Auth: r.Header.Get("Authorization"), Type: r.Header.Get("Content-Type")}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &call.Body)
		}
		cs.mu.Lock()
		cs.calls = append(cs.calls, call)
		cs.mu.Unlock()
		_, embeds := call.Body["input"]
		switch {
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{}})
		case embeds:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data":  []map[string]any{{"index": 0, "embedding": []float32{0.5, 0.5}}},
				"usage": map[string]int{"prompt_tokens": 3},
			})
		case call.Body["stream"] == true:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write(stream)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "hi"}}},
				"usage":   map[string]int{"prompt_tokens": 4, "completion_tokens": 1},
			})
		}
	}))
	t.Cleanup(cs.srv.Close)
	return cs
}

func (cs *contractServer) last(t *testing.T) contractCall {
	t.Helper()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if len(cs.calls) == 0 {
		t.Fatal("the server saw no request")
	}
	return cs.calls[len(cs.calls)-1]
}

func compatVendors() []domain.Vendor {
	var out []domain.Vendor
	for _, v := range domain.Vendors() {
		if v.Adapter == domain.AdapterOpenAI {
			out = append(out, v)
		}
	}
	return out
}

func TestContractEveryCompatVendorHasOneSavedStream(t *testing.T) {
	have := map[string]bool{}
	entries, err := os.ReadDir(contractDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		have[strings.TrimSuffix(e.Name(), ".sse")] = true
	}
	var want []string
	for _, v := range compatVendors() {
		want = append(want, v.Key)
		if !have[v.Key] {
			t.Errorf("%s has no saved stream", v.Key)
		}
		if _, ok := contractUsage[v.Key]; !ok {
			t.Errorf("%s has no expected usage", v.Key)
		}
	}
	for key := range have {
		if !slices.Contains(want, key) {
			t.Errorf("saved stream %q belongs to no compatible vendor", key)
		}
	}
	if len(contractUsage) != len(want) {
		t.Errorf("%d expected usages for %d vendors", len(contractUsage), len(want))
	}
}

func TestContractEveryCompatVendor(t *testing.T) {
	for _, v := range compatVendors() {
		v := v
		t.Run(v.Key, func(t *testing.T) {
			stream, err := os.ReadFile(filepath.Join(contractDir, v.Key+".sse"))
			if err != nil {
				t.Fatalf("no saved stream: %v", err)
			}
			cs := newContractServer(t, stream)
			to, err := url.Parse(cs.srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			rt := &redirect{to: to}
			a := Adapter{Client: &http.Client{Transport: rt}}
			plan := domain.PlanMetered
			if v.Local {
				plan = domain.PlanLocal
			}
			attempt := app.Attempt{BaseURL: v.BaseURL, Model: "m", Secret: "sk-test", PlanKind: plan}
			ctx := context.Background()

			// A streamed chat: URL, request shape, text deltas, final usage.
			var got []string
			usage, class, err := a.ChatStream(ctx, attempt, domain.Request{
				Messages:        []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
				MaxOutputTokens: 64,
			}, func(text string) error {
				got = append(got, text)
				return nil
			})
			if err != nil || class != domain.ClassOK {
				t.Fatalf("stream: class=%s err=%v", class, err)
			}
			if want := v.BaseURL + v.ChatPath; rt.lastAsked() != want {
				t.Errorf("stream URL = %s, want %s", rt.lastAsked(), want)
			}
			call := cs.last(t)
			if call.Method != http.MethodPost || call.Auth != "Bearer sk-test" || call.Type != "application/json" {
				t.Errorf("stream call = %+v", call)
			}
			body := call.Body
			if body["model"] != "m" || body["stream"] != true || body["max_tokens"] != float64(64) {
				t.Errorf("stream body = %v", body)
			}
			if opts, _ := body["stream_options"].(map[string]any); opts["include_usage"] != true {
				t.Errorf("stream_options = %v", body["stream_options"])
			}
			msgs, _ := body["messages"].([]any)
			if len(msgs) != 1 {
				t.Fatalf("messages = %v", body["messages"])
			}
			if msg, _ := msgs[0].(map[string]any); msg["role"] != "user" || msg["content"] != "hi" {
				t.Errorf("message = %v", msgs[0])
			}
			for _, key := range []string{"system", "tools"} {
				if _, has := body[key]; has {
					t.Errorf("body has %q though the request set none", key)
				}
			}
			if strings.Join(got, "|") != "Hel|lo" {
				t.Errorf("deltas = %v", got)
			}
			if usage != contractUsage[v.Key] {
				t.Errorf("usage = %+v, want %+v", usage, contractUsage[v.Key])
			}

			// A plain chat goes to the same path without the stream flags.
			text, _, class, err := a.Chat(ctx, attempt, domain.Request{
				Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
			})
			if err != nil || class != domain.ClassOK || text != "hi" {
				t.Fatalf("chat: %q class=%s err=%v", text, class, err)
			}
			if want := v.BaseURL + v.ChatPath; rt.lastAsked() != want {
				t.Errorf("chat URL = %s, want %s", rt.lastAsked(), want)
			}
			if _, streamed := cs.last(t).Body["stream"]; streamed {
				t.Errorf("a plain chat set the stream flag")
			}

			// The probe asks for the model list with the key.
			class, err = a.Probe(ctx, attempt)
			if err != nil || class != domain.ClassOK {
				t.Fatalf("probe: class=%s err=%v", class, err)
			}
			if want := v.BaseURL + v.ModelsPath; rt.lastAsked() != want {
				t.Errorf("probe URL = %s, want %s", rt.lastAsked(), want)
			}
			if call := cs.last(t); call.Method != http.MethodGet || call.Auth != "Bearer sk-test" {
				t.Errorf("probe call = %+v", call)
			}

			// An embed call goes to the vendor's embeddings path.
			vectors, _, class, err := a.Embed(ctx, attempt, []string{"one"})
			if err != nil || class != domain.ClassOK || len(vectors) != 1 {
				t.Fatalf("embed: %v class=%s err=%v", vectors, class, err)
			}
			if want := v.BaseURL + v.EmbedPath; rt.lastAsked() != want {
				t.Errorf("embed URL = %s, want %s", rt.lastAsked(), want)
			}
		})
	}
}

func TestContractGeminiHasNoV1InItsURLs(t *testing.T) {
	v, ok := domain.VendorFor("gemini")
	if !ok {
		t.Fatal("no gemini vendor")
	}
	stream, err := os.ReadFile(filepath.Join(contractDir, "gemini.sse"))
	if err != nil {
		t.Fatal(err)
	}
	cs := newContractServer(t, stream)
	to, _ := url.Parse(cs.srv.URL)
	rt := &redirect{to: to}
	a := Adapter{Client: &http.Client{Transport: rt}}
	_, class, err := a.ChatStream(context.Background(), app.Attempt{BaseURL: v.BaseURL, Model: "gemini-2.0-flash", Secret: "k"},
		domain.Request{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}, func(string) error { return nil })
	if err != nil || class != domain.ClassOK {
		t.Fatalf("class=%s err=%v", class, err)
	}
	if want := "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"; rt.lastAsked() != want {
		t.Fatalf("URL = %s, want %s", rt.lastAsked(), want)
	}
}

// testdata/recorded/gemini.sse is a real capture (2026-11-11, gemini-flash-lite-latest,
// stream_options.include_usage). Gemini repeats the same usage on every chunk,
// the first one that carries text included, and sends no usage-only chunk at
// the end, so usage has to be read as the latest value, not summed.
func TestContractGeminiRecordedStreamCountsRepeatedUsageOnce(t *testing.T) {
	stream, err := os.ReadFile(filepath.Join("testdata/recorded", "gemini.sse"))
	if err != nil {
		t.Fatal(err)
	}
	cs := newContractServer(t, stream)
	to, _ := url.Parse(cs.srv.URL)
	a := Adapter{Client: &http.Client{Transport: &redirect{to: to}}}
	v, _ := domain.VendorFor("gemini")
	var text string
	usage, class, err := a.ChatStream(context.Background(), app.Attempt{BaseURL: v.BaseURL, Model: "gemini-flash-lite-latest", Secret: "k"},
		domain.Request{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}, func(s string) error {
			text += s
			return nil
		})
	if err != nil || class != domain.ClassOK {
		t.Fatalf("class=%s err=%v", class, err)
	}
	if text != "One, two, three, four, five." {
		t.Errorf("text = %q", text)
	}
	if want := (domain.Usage{Arrived: true, Input: 9, Output: 10}); usage != want {
		t.Errorf("usage = %+v, want %+v", usage, want)
	}
}

func TestContractStreamWithoutUsageLeavesUsageNotArrived(t *testing.T) {
	// A vendor that sends no usage in its stream still completes: the text
	// arrives and the usage says it never did, so the ledger books no cost.
	var got []string
	srv := sseServer(t, false, streamDeltas...)
	a := Adapter{Client: srv.Client()}
	attempt, req := streamRequest(srv)
	usage, class, err := a.ChatStream(context.Background(), attempt, req, func(text string) error {
		got = append(got, text)
		return nil
	})
	if err != nil || class != domain.ClassOK {
		t.Fatalf("class=%s err=%v", class, err)
	}
	if usage.Arrived || usage != (domain.Usage{}) {
		t.Fatalf("usage = %+v", usage)
	}
	if strings.Join(got, "|") != "Hel|lo" {
		t.Fatalf("deltas = %v", got)
	}
}
