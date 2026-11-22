package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
)

const (
	PathChat       = "/upmo.gateway.v1.Gateway/Chat"
	PathChatStream = "/upmo.gateway.v1.Gateway/ChatStream"
	PathDecide     = "/upmo.gateway.v1.Gateway/Decide"
	PathEmbed      = "/upmo.gateway.v1.Gateway/Embed"
	PathModels     = "/upmo.gateway.v1.Gateway/Models"
)

// Handler serves the JSON debug surface on the Connect paths.
type Handler struct {
	Dispatch app.Dispatch
	Names    func(r *http.Request) ([]string, error)
	// Log records stream failures. It never receives a key.
	Log *slog.Logger
}

type actorBody struct {
	UserID       string `json:"user_id"`
	AgentID      string `json:"agent_id"`
	RunID        string `json:"run_id"`
	ParentRunID  string `json:"parent_run_id"`
	TeamID       string `json:"team_id"`
	Source       string `json:"source"`
	SourceID     string `json:"source_id"`
	AutomationID string `json:"automation_id"`
}

func (a actorBody) principal() domain.Principal {
	return domain.Principal{
		User:         a.UserID,
		Agent:        a.AgentID,
		Run:          a.RunID,
		Parent:       a.ParentRunID,
		TeamID:       a.TeamID,
		Source:       a.Source,
		SourceID:     a.SourceID,
		AutomationID: a.AutomationID,
	}
}

func (a actorBody) check() error {
	if a.Source == "" || (a.UserID == "" && a.Source != domain.SourceSystem) {
		return domain.Invalid("A source and payer are required unless the source is system.")
	}
	if !domain.ValidSource(a.Source) {
		return domain.Invalid("Choose a valid source.")
	}
	for _, id := range []string{a.UserID, a.SourceID, a.AutomationID} {
		if id != "" {
			if _, err := uuid.Parse(id); err != nil {
				return domain.Invalid("Use valid IDs for the payer and source.")
			}
		}
	}
	if a.Source == domain.SourceAutomation && a.AutomationID == "" {
		return domain.Invalid("An automation source needs an automation ID.")
	}
	return nil
}

type decideBody struct {
	Route         string   `json:"route"`
	Kind          string   `json:"kind"`
	Prompt        string   `json:"prompt"`
	Options       []string `json:"options"`
	CredentialRef string   `json:"credential_ref"`
	actorBody
}

type embedBody struct {
	Route         string   `json:"route"`
	Inputs        []string `json:"inputs"`
	CredentialRef string   `json:"credential_ref"`
	actorBody
}

type chatBody struct {
	Route           string            `json:"route"`
	Prompt          string            `json:"prompt"`
	System          string            `json:"system"`
	Tools           []domain.Tool     `json:"tools"`
	AllowTools      []string          `json:"allow_tools"`
	CoreTools       bool              `json:"core_tools"`
	Bundle          string            `json:"bundle"`
	Messages        []domain.Message  `json:"messages"`
	ToolChoice      domain.ToolChoice `json:"tool_choice"`
	CredentialRef   string            `json:"credential_ref"`
	MaxOutputTokens int               `json:"max_output_tokens"`
	// Pin, on a stream, is the account and model tried before the route.
	Pin *pinBody `json:"pin"`
	actorBody
}

func chatInput(body chatBody) domain.Input {
	in := domain.Input{
		Tools:           body.Tools,
		AllowTools:      body.AllowTools,
		CoreTools:       body.CoreTools,
		Bundle:          body.Bundle,
		System:          body.System,
		Messages:        body.Messages,
		ToolChoice:      body.ToolChoice,
		MaxOutputTokens: body.MaxOutputTokens,
	}
	if len(in.Messages) == 0 && body.Prompt != "" {
		in.Messages = []domain.Message{{Role: domain.RoleUser, Text: body.Prompt}}
	}
	return in
}

// pinBody is the optional pin of a chat stream: a model on an account, or on
// a provider the gateway resolves for the person.
type pinBody struct {
	AccountID string `json:"account_id"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
}

func (b chatBody) pin() domain.Pin {
	if b.Pin == nil {
		return domain.Pin{}
	}
	return domain.Pin{AccountID: b.Pin.AccountID, Provider: b.Pin.Provider, Model: b.Pin.Model}
}

func (h Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST "+PathChat, h.chat)
	mux.HandleFunc("POST "+PathChatStream, h.chatStream)
	mux.HandleFunc("POST "+PathDecide, h.decide)
	mux.HandleFunc("POST "+PathEmbed, h.embed)
	mux.HandleFunc("GET "+PathModels, h.models)
}

type usageSummary struct {
	InputTokens   int      `json:"input_tokens"`
	OutputTokens  int      `json:"output_tokens"`
	CacheRead     int      `json:"cache_read"`
	CacheWrite    int      `json:"cache_write"`
	Reasoning     int      `json:"reasoning"`
	Arrived       bool     `json:"arrived"`
	CostUSD       *float64 `json:"cost_usd"`
	CostMicroUSD  *int64   `json:"cost_micro_usd"`
	ContextWindow int      `json:"context_window"`
}

func toUsageSummary(u domain.Usage, costUSD *float64, costMicroUSD *int64, ctxWindow int) usageSummary {
	return usageSummary{
		InputTokens:   u.Input,
		OutputTokens:  u.Output,
		CacheRead:     u.CacheRead,
		CacheWrite:    u.CacheWrite,
		Reasoning:     u.Reasoning,
		Arrived:       u.Arrived,
		CostUSD:       costUSD,
		CostMicroUSD:  costMicroUSD,
		ContextWindow: ctxWindow,
	}
}

type decideResponse struct {
	YesNo  bool         `json:"yes"`
	Choice string       `json:"choice"`
	Usage  usageSummary `json:"usage"`
}

type chatResponse struct {
	Text          string            `json:"text"`
	ToolCalls     []domain.ToolCall `json:"tool_calls,omitempty"`
	StopReason    string            `json:"stop_reason,omitempty"`
	Model         string            `json:"model"`
	Usage         domain.Usage      `json:"usage"`
	CostUSD       *float64          `json:"cost_usd"`
	CostMicroUSD  *int64            `json:"cost_micro_usd"`
	ContextWindow int               `json:"context_window"`
}

func (h Handler) decide(w http.ResponseWriter, r *http.Request) {
	var body decideBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpserver.Fail(w, nil, "bad request", http.StatusBadRequest, err)
		return
	}
	if err := body.check(); err != nil {
		httpserver.Fail(w, nil, err.Error(), http.StatusBadRequest, err)
		return
	}
	result, err := h.Dispatch.DecideCall(r.Context(), body.Route, body.CredentialRef, body.principal(), domain.DecideRequest{
		Kind:    domain.Decision(body.Kind),
		Prompt:  body.Prompt,
		Options: body.Options,
	})
	if err != nil {
		h.failDispatch(w, body.principal(), err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, decideResponse{
		YesNo:  result.YesNo,
		Choice: result.Choice,
		Usage:  toUsageSummary(result.Usage, result.CostUSD, result.CostMicroUSD, result.ContextWindow),
	})
}

func (h Handler) chat(w http.ResponseWriter, r *http.Request) {
	var body chatBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpserver.Fail(w, nil, "bad request", http.StatusBadRequest, err)
		return
	}
	if err := body.check(); err != nil {
		httpserver.Fail(w, nil, err.Error(), http.StatusBadRequest, err)
		return
	}
	res, err := h.Dispatch.ChatCall(r.Context(), body.Route, body.CredentialRef, body.principal(), chatInput(body))
	if err != nil {
		h.failDispatch(w, body.principal(), err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, chatResponse{
		Text:          res.Text,
		ToolCalls:     res.ToolCalls,
		StopReason:    res.StopReason,
		Model:         res.Model,
		Usage:         res.Usage,
		CostUSD:       res.CostUSD,
		CostMicroUSD:  res.CostMicroUSD,
		ContextWindow: res.ContextWindow,
	})
}

// writeDeadline is how long one frame of a chat stream may take to write.
// The server's WriteTimeout counts from the request header and would cut a
// long reply off, so every frame sets a new deadline.
const writeDeadline = 30 * time.Second

// chatStream serves one streamed chat reply as application/x-ndjson: one
// JSON object per line, flushed as written:
//
//	{"delta":"..."}
//	{"tool_call":{"index":0,"id":"...","name":"...","arguments_delta":"..."}}
//	{"attempt":{"failed_model":"...","failure_class":"...","next_model":"..."}}
//	{"done":{"model":"...","input_tokens":n,"output_tokens":n,"cache_read":n,"cache_write":n,"reasoning":n,"arrived":true,"cost_usd":x|null,"cost_micro_usd":n|null,"context_window":n}}
//	{"error":{"failure_class":"...","model":"...","message":"..."}}
//
// Exactly one done or error frame ends the stream. The error message is a
// fixed phrase by failure class; the real error is logged server-side. A
// refusal before the first frame, such as a bad body or an unknown route,
// stays a plain HTTP error as for Chat.
func (h Handler) chatStream(w http.ResponseWriter, r *http.Request) {
	var body chatBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpserver.Fail(w, nil, "bad request", http.StatusBadRequest, err)
		return
	}
	if err := body.check(); err != nil {
		httpserver.Fail(w, nil, err.Error(), http.StatusBadRequest, err)
		return
	}
	in := chatInput(body)
	who := body.principal()
	out := &chatStreamOut{w: w, rc: http.NewResponseController(w), log: h.Log, who: who}
	if err := h.streamReply(r.Context(), w, body, in, out); err != nil {
		switch {
		case !out.started:
			h.failDispatch(w, who, err)
		case !out.ended:
			var limitErr domain.LimitReached
			if errors.As(err, &limitErr) {
				out.limitFailure(limitErr)
			} else {
				// The call stopped between frames; the stream still needs its end.
				out.internalFailure(err)
			}
		}
	}
}

// streamReply runs the dispatch behind one stream. A panic in the stream is
// recovered and logged: with frames already sent the stream still ends with
// one error frame; before any frame it is a plain 500.
func (h Handler) streamReply(ctx context.Context, w http.ResponseWriter, body chatBody, in domain.Input, out *chatStreamOut) error {
	defer func() {
		rec := recover()
		if rec == nil {
			return
		}
		err := fmt.Errorf("gateway: stream panic: %v", rec)
		switch {
		case out.started && !out.ended:
			out.internalFailure(err)
		case !out.started:
			httpserver.Fail(w, h.Log, "internal", http.StatusInternalServerError, err)
		}
	}()
	return h.Dispatch.ChatStreamPinned(ctx, body.Route, body.CredentialRef, body.principal(), in, body.pin(), body.MaxOutputTokens, out)
}

type (
	chatStreamDelta struct {
		Delta string `json:"delta"`
	}
	chatStreamToolCall struct {
		ToolCall app.ToolCallDelta `json:"tool_call"`
	}
	chatStreamAttempt struct {
		Attempt chatStreamNotice `json:"attempt"`
	}
	chatStreamNotice struct {
		FailedModel  string `json:"failed_model"`
		FailureClass string `json:"failure_class"`
		NextModel    string `json:"next_model"`
	}
	chatStreamDone struct {
		Done chatStreamResult `json:"done"`
	}
	chatStreamResult struct {
		Model         string            `json:"model"`
		InputTokens   int               `json:"input_tokens"`
		OutputTokens  int               `json:"output_tokens"`
		CacheRead     int               `json:"cache_read"`
		CacheWrite    int               `json:"cache_write"`
		Reasoning     int               `json:"reasoning"`
		Arrived       bool              `json:"arrived"`
		CostUSD       *float64          `json:"cost_usd"`
		CostMicroUSD  *int64            `json:"cost_micro_usd"`
		ContextWindow int               `json:"context_window"`
		ToolCalls     []domain.ToolCall `json:"tool_calls,omitempty"`
		StopReason    string            `json:"stop_reason,omitempty"`
	}
	chatStreamError struct {
		Error chatStreamFailure `json:"error"`
	}
	chatStreamFailure struct {
		FailureClass string `json:"failure_class"`
		Model        string `json:"model"`
		Message      string `json:"message"`
	}
)

// chatStreamOut is the StreamSink that writes one chat stream to the wire.
// The status line waits for the first frame, so a refusal before any frame
// can still answer with a plain status. A client disconnect shows up as a
// write error, which stops the provider call through Delta or Notice.
type chatStreamOut struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	enc     *json.Encoder
	log     *slog.Logger
	who     domain.Principal
	started bool
	ended   bool
}

func (s *chatStreamOut) frame(v any) error {
	if !s.started {
		s.started = true
		header := s.w.Header()
		header.Set("Content-Type", "application/x-ndjson")
		header.Set("Cache-Control", "no-store")
		s.w.WriteHeader(http.StatusOK)
		s.enc = json.NewEncoder(s.w)
	}
	if err := s.rc.SetWriteDeadline(time.Now().Add(writeDeadline)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if err := s.enc.Encode(v); err != nil {
		return err
	}
	return s.rc.Flush()
}

func (s *chatStreamOut) Delta(text string) error {
	return s.frame(chatStreamDelta{Delta: text})
}

func (s *chatStreamOut) ToolCallDelta(delta app.ToolCallDelta) error {
	return s.frame(chatStreamToolCall{ToolCall: delta})
}

func (s *chatStreamOut) Notice(notice app.StreamNotice) error {
	return s.frame(chatStreamAttempt{Attempt: chatStreamNotice{
		FailedModel:  notice.FailedModel,
		FailureClass: string(notice.Class),
		NextModel:    notice.NextModel,
	}})
}

func (s *chatStreamOut) Done(done app.StreamDone) {
	s.ended = true
	_ = s.frame(chatStreamDone{Done: chatStreamResult{
		Model:         done.Model,
		InputTokens:   done.Usage.Input,
		OutputTokens:  done.Usage.Output,
		CacheRead:     done.Usage.CacheRead,
		CacheWrite:    done.Usage.CacheWrite,
		Reasoning:     done.Usage.Reasoning,
		Arrived:       done.Usage.Arrived,
		CostUSD:       done.CostUSD,
		CostMicroUSD:  done.CostMicroUSD,
		ContextWindow: done.ContextWindow,
		ToolCalls:     done.ToolCalls,
		StopReason:    done.StopReason,
	}})
}

func (s *chatStreamOut) Fail(fail app.StreamFail) {
	s.ended = true
	s.logStream("chat stream failed", fail.Class, fail.Model, fail.Err)
	_ = s.frame(chatStreamError{Error: chatStreamFailure{
		FailureClass: string(fail.Class),
		Model:        fail.Model,
		Message:      wireMessage(fail.Class),
	}})
}

// internalFailure ends a stream that broke on the gateway side. The original
// error is logged; the frame carries the fixed internal phrase.
func (s *chatStreamOut) internalFailure(err error) {
	s.ended = true
	s.logStream("chat stream broke", domain.ClassInternal, "", err)
	_ = s.frame(chatStreamError{Error: chatStreamFailure{
		FailureClass: string(domain.ClassInternal),
		Message:      "the gateway failed",
	}})
}

func (s *chatStreamOut) limitFailure(err domain.LimitReached) {
	s.ended = true
	logLimitReached(s.log, s.who, err)
	_ = s.frame(chatStreamError{Error: chatStreamFailure{
		FailureClass: "limit_reached",
		Message:      err.Error(),
	}})
}

func (h Handler) failDispatch(w http.ResponseWriter, who domain.Principal, err error) {
	var limitErr domain.LimitReached
	switch {
	case errors.As(err, &limitErr):
		logLimitReached(h.Log, who, limitErr)
		httpserver.Fail(w, nil, limitErr.Error(), http.StatusTooManyRequests, err)
	default:
		httpserver.Fail(w, nil, err.Error(), http.StatusBadRequest, err)
	}
}

func logLimitReached(log *slog.Logger, who domain.Principal, err domain.LimitReached) {
	if log == nil {
		return
	}
	log.Warn("gateway spend limit reached",
		"kind", err.Kind,
		"subject_id", limitSubject(err.Kind, who),
		"limit_micro_usd", err.Limit,
		"spent_micro_usd", err.Spent,
		"held_micro_usd", err.Held,
	)
}

func limitSubject(kind string, who domain.Principal) string {
	switch kind {
	case domain.LimitPerson, domain.LimitPersonDefault:
		return who.User
	case domain.LimitAutomation:
		return who.AutomationID
	default:
		return ""
	}
}

// logStream records the real error server-side; it never reaches the wire.
func (s *chatStreamOut) logStream(msg string, class domain.FailureClass, model string, err error) {
	if s.log != nil {
		s.log.Error(msg, "class", class, "model", model, "err", err)
	}
}

// wireMessage is the fixed public message for a failed stream, by class. It
// never carries a prompt, a key or a provider body; the real error is
// logged server-side instead.
func wireMessage(class domain.FailureClass) string {
	switch class {
	case domain.ClassRateLimit:
		return "the provider rate limited the request"
	case domain.ClassOverloaded:
		return "the provider was overloaded"
	case domain.ClassTimeout:
		return "the provider timed out"
	case domain.ClassCredential:
		return "the account credential was refused"
	case domain.ClassModelAccess:
		return "the model was not available"
	case domain.ClassCaller:
		return "the provider refused the request"
	case domain.ClassCanceled:
		return "the request was canceled"
	case domain.ClassInternal:
		return "the gateway failed"
	default:
		return "the provider failed"
	}
}

func (h Handler) embed(w http.ResponseWriter, r *http.Request) {
	var body embedBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpserver.Fail(w, nil, "bad request", http.StatusBadRequest, err)
		return
	}
	if err := body.check(); err != nil {
		httpserver.Fail(w, nil, err.Error(), http.StatusBadRequest, err)
		return
	}
	model, vectors, err := h.Dispatch.EmbedCall(r.Context(), body.Route, body.CredentialRef, body.principal(), body.Inputs)
	if err != nil {
		h.failDispatch(w, body.principal(), err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"model": model, "vectors": vectors})
}

func (h Handler) models(w http.ResponseWriter, r *http.Request) {
	if h.Names == nil {
		httpserver.WriteJSON(w, http.StatusOK, map[string]any{"routes": []string{}})
		return
	}
	names, err := h.Names(r)
	if err != nil {
		httpserver.Fail(w, nil, "unavailable", http.StatusInternalServerError, err)
		return
	}
	if names == nil {
		names = []string{}
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"routes": names})
}
