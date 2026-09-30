// Package httpapi serves the SandboxProvider port as JSON on the Connect
// paths. cmd/sandboxd wraps it with the service HMAC.
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/runtime"
)

const (
	PathCreate    = "/upmo.sandboxd.v1.Runtime/Create"
	PathExec      = "/upmo.sandboxd.v1.Runtime/Exec"
	PathPTY       = "/upmo.sandboxd.v1.Runtime/PTY"
	PathReadFile  = "/upmo.sandboxd.v1.Runtime/ReadFile"
	PathWriteFile = "/upmo.sandboxd.v1.Runtime/WriteFile"
	PathExpose    = "/upmo.sandboxd.v1.Runtime/Expose"
	PathPause     = "/upmo.sandboxd.v1.Runtime/Pause"
	PathDestroy   = "/upmo.sandboxd.v1.Runtime/Destroy"
)

const bodyLimit = 1 << 20

// Handler serves one provider.
type Handler struct {
	Provider runtime.SandboxProvider
	Log      *slog.Logger
}

type credentialBody struct {
	Sentinel string `json:"sentinel"`
	Ref      string `json:"ref"`
	Host     string `json:"host"`
}

type createBody struct {
	Task        string           `json:"task_id"`
	Image       string           `json:"image"`
	Tier        string           `json:"tier"`
	TTLSeconds  int64            `json:"ttl_seconds"`
	Allow       []string         `json:"allow"`
	Credentials []credentialBody `json:"credentials"`
}

type shapeBody struct {
	Scratch     string `json:"scratch"`
	CPU         int    `json:"cpu"`
	Burst       int    `json:"burst"`
	PIDs        int    `json:"pids"`
	ExecSeconds int64  `json:"exec_seconds"`
}

type sandboxBody struct {
	ID      string    `json:"id"`
	Tier    string    `json:"tier"`
	Shape   shapeBody `json:"shape"`
	Expires time.Time `json:"expires"`
}

type idBody struct {
	ID string `json:"id"`
}

// Mount registers every SandboxProvider route on mux. Routes whose driver
// method is not built yet answer 501.
func (h Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST "+PathCreate, h.create)
	mux.HandleFunc("POST "+PathPause, h.pause)
	mux.HandleFunc("POST "+PathDestroy, h.destroy)
	for path, name := range map[string]string{
		PathExec:      "Exec",
		PathPTY:       "PTY",
		PathReadFile:  "ReadFile",
		PathWriteFile: "WriteFile",
		PathExpose:    "Expose",
	} {
		mux.HandleFunc("POST "+path, notBuilt(name))
	}
}

func (h Handler) create(w http.ResponseWriter, r *http.Request) {
	var body createBody
	if !decode(w, r, &body) {
		return
	}
	// The domain sets no TTL ceiling; this bound only keeps the duration from overflowing.
	if body.TTLSeconds <= 0 || body.TTLSeconds > math.MaxInt64/int64(time.Second) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	spec := runtime.Spec{
		Task:  body.Task,
		Image: body.Image,
		Tier:  body.Tier,
		TTL:   time.Duration(body.TTLSeconds) * time.Second,
		Allow: body.Allow,
	}
	for _, c := range body.Credentials {
		spec.Credentials = append(spec.Credentials, runtime.Credential{Sentinel: c.Sentinel, Ref: c.Ref, Host: c.Host})
	}
	box, err := h.Provider.Create(r.Context(), spec)
	if err != nil {
		h.fail(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, sandboxBody{
		ID:   box.ID,
		Tier: box.Tier,
		Shape: shapeBody{
			Scratch:     box.Shape.Scratch,
			CPU:         box.Shape.CPU,
			Burst:       box.Shape.Burst,
			PIDs:        box.Shape.PIDs,
			ExecSeconds: int64(box.Shape.Exec / time.Second),
		},
		Expires: box.Expires,
	})
}

func (h Handler) pause(w http.ResponseWriter, r *http.Request) {
	var body idBody
	if !decode(w, r, &body) {
		return
	}
	if err := h.Provider.Pause(r.Context(), body.ID); err != nil {
		h.fail(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct{}{})
}

func (h Handler) destroy(w http.ResponseWriter, r *http.Request) {
	var body idBody
	if !decode(w, r, &body) {
		return
	}
	if err := h.Provider.Destroy(r.Context(), body.ID); err != nil {
		h.fail(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct{}{})
}

func notBuilt(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, name+" is not built yet", http.StatusNotImplemented)
	}
}

func decode(w http.ResponseWriter, r *http.Request, dest any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
	if err := json.NewDecoder(r.Body).Decode(dest); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

func (h Handler) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, runtime.ErrSpec):
		http.Error(w, "bad request", http.StatusBadRequest)
	case errors.Is(err, runtime.ErrIsolation):
		http.Error(w, "no driver meets this tier", http.StatusConflict)
	case errors.Is(err, runtime.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, runtime.ErrUnsupported):
		http.Error(w, "not built yet", http.StatusNotImplemented)
	default:
		httpserver.Fail(w, h.Log, "unavailable", http.StatusInternalServerError, err)
	}
}
