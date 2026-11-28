// Package httpapi serves the SandboxProvider port as JSON on the Connect
// paths. cmd/sandboxd wraps it with the service HMAC.
//
// # Wire format
//
// Every route takes a POST with a JSON body. A unary route answers 200 with
// a JSON body. A refusal answers another status with a plain text body and
// a machine-readable code in the Upmo-Error header (the Code constants), so
// brain's client can tell a missing file from a missing sandbox.
//
// Exec takes {"id","args","dir","timeout_ms"}. A zero timeout_ms means the
// runtime cap. Once the command writes output or ends, Exec answers 200
// with Content-Type application/x-ndjson: one JSON object per line, flushed
// as it is written, in the order the command wrote its output.
//
//	{"type":"output","stream":"stdout","data":"<base64>"}
//	{"type":"output","stream":"stderr","data":"<base64>"}
//	{"type":"exit","code":3}
//	{"type":"error","error":"timeout"}
//
// An output frame carries at most FrameData bytes before base64; a larger
// chunk is split across frames. Exactly one exit or error frame ends the
// stream, and its error is one of the Code constants. A stream that ends
// without either was cut short. A refusal the provider reports before any
// output, such as an unknown or paused sandbox, answers with a status and
// code instead of a stream.
//
// ReadFile takes {"id","path"} and answers {"data":"<base64>"}. WriteFile
// takes {"id","path","data":"<base64>"} and answers {}. Either carries at
// most runtime.MaxFile bytes before base64.
package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/runtime"
)

const (
	PathCreate      = "/upmo.sandboxd.v1.Runtime/Create"
	PathExec        = "/upmo.sandboxd.v1.Runtime/Exec"
	PathPTY         = "/upmo.sandboxd.v1.Runtime/PTY"
	PathReadFile    = "/upmo.sandboxd.v1.Runtime/ReadFile"
	PathWriteFile   = "/upmo.sandboxd.v1.Runtime/WriteFile"
	PathExpose      = "/upmo.sandboxd.v1.Runtime/Expose"
	PathPause       = "/upmo.sandboxd.v1.Runtime/Pause"
	PathDestroy     = "/upmo.sandboxd.v1.Runtime/Destroy"
	PathStartEditor = "/upmo.sandboxd.v1.Runtime/StartEditor"
	PathStopEditor  = "/upmo.sandboxd.v1.Runtime/StopEditor"
)

// HeaderError names the response header that carries a refusal's code.
const HeaderError = "Upmo-Error"

// Codes a refusal or an Exec error frame carries.
const (
	CodeSpec        = "spec"
	CodeIsolation   = "isolation"
	CodeNotFound    = "not_found"
	CodeUnsupported = "unsupported"
	CodePaused      = "paused"
	CodeTooLarge    = "too_large"
	CodeNotFile     = "not_file"
	CodeNoFile      = "no_file"
	CodeTimeout     = "timeout"
	CodeUnavailable = "unavailable"
	CodeEditor      = "editor"
)

// ContentTypeStream is the Content-Type of an Exec stream.
const ContentTypeStream = "application/x-ndjson"

// FrameData is the most output bytes one Exec frame carries.
const FrameData = 32 << 10

const bodyLimit = 1 << 20

// FileLimit bounds a WriteFile body: MaxFile in base64 plus room for the
// id, the path and the JSON around them. It is also the signed-request cap.
var FileLimit = int64(base64.StdEncoding.EncodedLen(runtime.MaxFile)) + bodyLimit

// execGrace is the time an exec takes around the command itself: creating
// it, attaching to it and waiting for the engine to report the exit code.
const execGrace = 30 * time.Second

// Handler serves one provider.
type Handler struct {
	Provider runtime.SandboxProvider
	Log      *slog.Logger
}

type credentialBody struct {
	Sentinel string   `json:"sentinel"`
	Ref      string   `json:"ref"`
	Host     string   `json:"host"`
	Methods  []string `json:"methods"`
	Paths    []string `json:"paths"`
}

type createBody struct {
	Task        string           `json:"task_id"`
	Image       string           `json:"image"`
	Tier        string           `json:"tier"`
	TTLSeconds  int64            `json:"ttl_seconds"`
	Allow       []string         `json:"allow"`
	Credentials []credentialBody `json:"credentials"`
	ProxySecret []byte           `json:"proxy_secret"`
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

type startEditorBody struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type urlBody struct {
	URL string `json:"url"`
}

type execBody struct {
	ID        string   `json:"id"`
	Args      []string `json:"args"`
	Dir       string   `json:"dir"`
	TimeoutMS int64    `json:"timeout_ms"`
}

// frame is one line of an Exec stream. Type is output, exit or error.
type frame struct {
	Type   string `json:"type"`
	Stream string `json:"stream,omitempty"`
	Data   []byte `json:"data,omitempty"`
	Code   *int   `json:"code,omitempty"`
	Error  string `json:"error,omitempty"`
}

type fileBody struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Data []byte `json:"data"`
}

type dataBody struct {
	Data []byte `json:"data"`
}

// Mount registers every SandboxProvider route on mux. Routes whose driver
// method is not built yet answer 501.
func (h Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST "+PathCreate, h.create)
	mux.HandleFunc("POST "+PathExec, h.exec)
	mux.HandleFunc("POST "+PathReadFile, h.readFile)
	mux.HandleFunc("POST "+PathWriteFile, h.writeFile)
	mux.HandleFunc("POST "+PathPause, h.pause)
	mux.HandleFunc("POST "+PathDestroy, h.destroy)
	mux.HandleFunc("POST "+PathStartEditor, h.startEditor)
	mux.HandleFunc("POST "+PathStopEditor, h.stopEditor)
	for path, name := range map[string]string{
		PathPTY:    "PTY",
		PathExpose: "Expose",
	} {
		mux.HandleFunc("POST "+path, notBuilt(name))
	}
}

func (h Handler) create(w http.ResponseWriter, r *http.Request) {
	var body createBody
	if !h.decode(w, r, &body, bodyLimit) {
		return
	}
	// The domain sets no TTL ceiling; this bound only keeps the duration from overflowing.
	if body.TTLSeconds <= 0 || body.TTLSeconds > math.MaxInt64/int64(time.Second) {
		h.fail(w, runtime.ErrSpec)
		return
	}
	// Brain chose the proxy secret and stored its hash; without it sandboxd
	// could not bind the sandbox, so nothing is created.
	if len(body.ProxySecret) != 32 {
		h.fail(w, runtime.ErrSpec)
		return
	}
	spec := runtime.Spec{
		Task:        body.Task,
		Image:       body.Image,
		Tier:        body.Tier,
		TTL:         time.Duration(body.TTLSeconds) * time.Second,
		Allow:       body.Allow,
		ProxySecret: body.ProxySecret,
	}
	for _, c := range body.Credentials {
		spec.Credentials = append(spec.Credentials, runtime.Credential{Sentinel: c.Sentinel, Ref: c.Ref, Host: c.Host, Methods: c.Methods, Paths: c.Paths})
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

// exec runs a command and streams its output as NDJSON frames; the package
// comment describes them.
func (h Handler) exec(w http.ResponseWriter, r *http.Request) {
	var body execBody
	if !h.decode(w, r, &body, bodyLimit) {
		return
	}
	// The provider checks the runtime cap; this bound only keeps the duration from overflowing.
	if body.TimeoutMS < 0 || body.TimeoutMS > math.MaxInt64/int64(time.Millisecond) {
		h.fail(w, runtime.ErrSpec)
		return
	}
	cmd := runtime.Command{
		Args:    body.Args,
		Dir:     body.Dir,
		Timeout: time.Duration(body.TimeoutMS) * time.Millisecond,
	}

	// The server's write timeout suits unary calls and would cut a stream
	// mid-command, since a command may run up to the runtime cap. This
	// response gets the command's own budget instead. The read deadline
	// moves with it, so nothing on the connection expires under a running
	// command; the body has been read by now. A writer that cannot take
	// deadlines, such as a test recorder, keeps none.
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(execBudget(cmd.Timeout))
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)

	s := &stream{w: w, rc: rc}
	exit, err := h.Provider.Exec(r.Context(), body.ID, cmd, s.output)
	s.finish(h, exit, err)
}

// execBudget is how long an Exec response may stay open: the command's
// timeout, or the runtime cap when it has none, plus execGrace.
func execBudget(timeout time.Duration) time.Duration {
	limit := runtime.ResourceShape().Exec
	if timeout > 0 && timeout < limit {
		limit = timeout
	}
	return limit + execGrace
}

// stream writes the frames of one Exec response. The status line waits for
// the first output, so a refusal before any output can still answer with a
// status and code.
type stream struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	rc      *http.ResponseController
	enc     *json.Encoder
	started bool
	ended   bool
}

var errEnded = errors.New("httpapi: exec stream has ended")

// output is the provider's out callback. A write error, which means brain
// has gone, is returned so the provider stops the command.
func (s *stream) output(o runtime.Output) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return errEnded
	}
	if o.Stream != "stdout" && o.Stream != "stderr" {
		return errors.New("httpapi: unknown output stream")
	}
	for data := o.Data; len(data) > 0; {
		n := min(len(data), FrameData)
		if err := s.write(frame{Type: "output", Stream: o.Stream, Data: data[:n]}); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

// finish ends the stream with an exit or error frame, or answers a refusal
// with a status when no output was written.
func (s *stream) finish(h Handler, exit runtime.Exit, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
	if err == nil {
		code := exit.Code
		_ = s.write(frame{Type: "exit", Code: &code})
		return
	}
	if !s.started {
		h.fail(s.w, err)
		return
	}
	code, _, _ := classify(err)
	if code == CodeUnavailable && !errors.Is(err, context.Canceled) && h.Log != nil {
		h.Log.Error("exec", "err", err)
	}
	_ = s.write(frame{Type: "error", Error: code})
}

// write sends one frame and flushes it. The caller holds s.mu.
func (s *stream) write(f frame) error {
	if !s.started {
		s.started = true
		s.w.Header().Set("Content-Type", ContentTypeStream)
		s.w.Header().Set("Cache-Control", "no-store")
		s.w.WriteHeader(http.StatusOK)
		s.enc = json.NewEncoder(s.w)
	}
	if err := s.enc.Encode(f); err != nil {
		return err
	}
	if err := s.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

func (h Handler) readFile(w http.ResponseWriter, r *http.Request) {
	var body fileBody
	if !h.decode(w, r, &body, bodyLimit) {
		return
	}
	data, err := h.Provider.ReadFile(r.Context(), body.ID, body.Path)
	if err != nil {
		h.fail(w, err)
		return
	}
	if len(data) > runtime.MaxFile {
		h.fail(w, runtime.ErrTooLarge)
		return
	}
	if data == nil {
		// An empty file is "", never null, so the client can tell it from a missing field.
		data = []byte{}
	}
	httpserver.WriteJSON(w, http.StatusOK, dataBody{Data: data})
}

func (h Handler) writeFile(w http.ResponseWriter, r *http.Request) {
	var body fileBody
	if !h.decode(w, r, &body, FileLimit) {
		return
	}
	if len(body.Data) > runtime.MaxFile {
		h.fail(w, runtime.ErrTooLarge)
		return
	}
	if err := h.Provider.WriteFile(r.Context(), body.ID, body.Path, body.Data); err != nil {
		h.fail(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct{}{})
}

func (h Handler) pause(w http.ResponseWriter, r *http.Request) {
	var body idBody
	if !h.decode(w, r, &body, bodyLimit) {
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
	if !h.decode(w, r, &body, bodyLimit) {
		return
	}
	if err := h.Provider.Destroy(r.Context(), body.ID); err != nil {
		h.fail(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct{}{})
}

func (h Handler) startEditor(w http.ResponseWriter, r *http.Request) {
	var body startEditorBody
	if !h.decode(w, r, &body, bodyLimit) {
		return
	}
	ep, err := h.Provider.StartEditor(r.Context(), body.ID, body.Status)
	if err != nil {
		h.fail(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, urlBody{URL: ep.URL})
}

func (h Handler) stopEditor(w http.ResponseWriter, r *http.Request) {
	var body idBody
	if !h.decode(w, r, &body, bodyLimit) {
		return
	}
	if err := h.Provider.StopEditor(r.Context(), body.ID); err != nil {
		h.fail(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct{}{})
}

func notBuilt(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(HeaderError, CodeUnsupported)
		http.Error(w, name+" is not built yet", http.StatusNotImplemented)
	}
}

// decode reads a JSON body of at most limit bytes into dest. A body over
// the limit is too large; anything else that does not decode is a bad spec.
func (h Handler) decode(w http.ResponseWriter, r *http.Request, dest any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := json.NewDecoder(r.Body).Decode(dest); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			h.fail(w, runtime.ErrTooLarge)
		} else {
			h.fail(w, runtime.ErrSpec)
		}
		return false
	}
	return true
}

// classify returns the wire code, status and plain text for a provider error.
func classify(err error) (code string, status int, text string) {
	switch {
	case errors.Is(err, runtime.ErrSpec):
		return CodeSpec, http.StatusBadRequest, "bad request"
	case errors.Is(err, runtime.ErrIsolation):
		return CodeIsolation, http.StatusConflict, "no driver meets this tier"
	case errors.Is(err, runtime.ErrNotFound):
		return CodeNotFound, http.StatusNotFound, "not found"
	case errors.Is(err, runtime.ErrUnsupported):
		return CodeUnsupported, http.StatusNotImplemented, "not built yet"
	case errors.Is(err, runtime.ErrPaused):
		return CodePaused, http.StatusConflict, "sandbox is paused"
	case errors.Is(err, runtime.ErrTooLarge):
		return CodeTooLarge, http.StatusRequestEntityTooLarge, "file is too large"
	case errors.Is(err, runtime.ErrNotFile):
		return CodeNotFile, http.StatusUnprocessableEntity, "not a regular file"
	case errors.Is(err, runtime.ErrEditor):
		return CodeEditor, http.StatusConflict, "editor refused"
	case errors.Is(err, fs.ErrNotExist):
		return CodeNoFile, http.StatusNotFound, "no such file"
	case errors.Is(err, context.DeadlineExceeded):
		return CodeTimeout, http.StatusGatewayTimeout, "command timed out"
	default:
		return CodeUnavailable, http.StatusInternalServerError, "unavailable"
	}
}

func (h Handler) fail(w http.ResponseWriter, err error) {
	code, status, text := classify(err)
	w.Header().Set(HeaderError, code)
	if code == CodeUnavailable && !errors.Is(err, context.Canceled) {
		httpserver.Fail(w, h.Log, text, status, err)
		return
	}
	http.Error(w, text, status)
}
