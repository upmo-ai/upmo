package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
)

const (
	PathChat   = "/upmo.gateway.v1.Gateway/Chat"
	PathDecide = "/upmo.gateway.v1.Gateway/Decide"
	PathEmbed  = "/upmo.gateway.v1.Gateway/Embed"
	PathModels = "/upmo.gateway.v1.Gateway/Models"
)

// Handler serves the JSON debug surface on the Connect paths.
type Handler struct {
	Dispatch app.Dispatch
	Names    func(r *http.Request) ([]string, error)
}

type decideBody struct {
	Route         string   `json:"route"`
	Kind          string   `json:"kind"`
	Prompt        string   `json:"prompt"`
	Options       []string `json:"options"`
	CredentialRef string   `json:"credential_ref"`
}

type chatBody struct {
	Route         string           `json:"route"`
	Prompt        string           `json:"prompt"`
	System        string           `json:"system"`
	Tools         []domain.Tool    `json:"tools"`
	AllowTools    []string         `json:"allow_tools"`
	Messages      []domain.Message `json:"messages"`
	CredentialRef string           `json:"credential_ref"`
}

func (h Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST "+PathChat, h.chat)
	mux.HandleFunc("POST "+PathDecide, h.decide)
	mux.HandleFunc("POST "+PathEmbed, h.embed)
	mux.HandleFunc("GET "+PathModels, h.models)
}

func (h Handler) decide(w http.ResponseWriter, r *http.Request) {
	var body decideBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpserver.Fail(w, nil, "bad request", http.StatusBadRequest, err)
		return
	}
	result, err := h.Dispatch.DecideCall(r.Context(), body.Route, body.CredentialRef, domain.DecideRequest{
		Kind:    domain.Decision(body.Kind),
		Prompt:  body.Prompt,
		Options: body.Options,
	})
	if err != nil {
		httpserver.Fail(w, nil, err.Error(), http.StatusBadRequest, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"yes": result.YesNo, "choice": result.Choice})
}

func (h Handler) chat(w http.ResponseWriter, r *http.Request) {
	var body chatBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpserver.Fail(w, nil, "bad request", http.StatusBadRequest, err)
		return
	}
	in := domain.Input{
		Tools:      body.Tools,
		AllowTools: body.AllowTools,
		System:     body.System,
		Messages:   body.Messages,
	}
	if len(in.Messages) == 0 && body.Prompt != "" {
		in.Messages = []domain.Message{{Role: domain.RoleUser, Text: body.Prompt}}
	}
	text, err := h.Dispatch.ChatCall(r.Context(), body.Route, body.CredentialRef, in)
	if err != nil {
		httpserver.Fail(w, nil, err.Error(), http.StatusBadRequest, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]string{"text": text})
}

func (h Handler) embed(w http.ResponseWriter, _ *http.Request) {
	httpserver.Fail(w, nil, "embed is not served yet", http.StatusNotImplemented)
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
