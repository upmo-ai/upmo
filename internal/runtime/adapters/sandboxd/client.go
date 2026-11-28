// Package sandboxd is brain's client for the sandboxd process. It signs each
// call with the service HMAC for the sandboxd audience and does
// not import the sandboxd process or any container engine.
//
// The wire format is the one internal/sandboxd/adapters/http documents: JSON
// bodies, a refusal code in the Upmo-Error header, and an Exec response
// streamed as NDJSON frames that end with one exit or error frame. This
// package cannot import that one, so the paths, codes and frame shape are
// repeated here and the sandboxd tests run this client against the real
// routes.
package sandboxd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/runtime/app"
	"github.com/upmo-ai/upmo/internal/runtime/domain"
)

const (
	pathCreate      = "/upmo.sandboxd.v1.Runtime/Create"
	pathExec        = "/upmo.sandboxd.v1.Runtime/Exec"
	pathPTY         = "/upmo.sandboxd.v1.Runtime/PTY"
	pathReadFile    = "/upmo.sandboxd.v1.Runtime/ReadFile"
	pathWriteFile   = "/upmo.sandboxd.v1.Runtime/WriteFile"
	pathExpose      = "/upmo.sandboxd.v1.Runtime/Expose"
	pathPause       = "/upmo.sandboxd.v1.Runtime/Pause"
	pathDestroy     = "/upmo.sandboxd.v1.Runtime/Destroy"
	pathStartEditor = "/upmo.sandboxd.v1.Runtime/StartEditor"
	pathStopEditor  = "/upmo.sandboxd.v1.Runtime/StopEditor"
)

// headerError carries a refusal's code; contentTypeStream marks an Exec stream.
const (
	headerError       = "Upmo-Error"
	contentTypeStream = "application/x-ndjson"
)

const bodyLimit = 1 << 20

// unaryTimeout bounds every call but Exec, which is bounded by its command.
const unaryTimeout = 30 * time.Second

// execGrace is how long past the command's own timeout the client waits for
// sandboxd to end the stream. sandboxd allows itself less, so it reports a
// timeout before this backstop fires.
const execGrace = time.Minute

// fileLimit bounds a ReadFile response: MaxFile in base64 plus the JSON
// around it.
var fileLimit = int64(base64.StdEncoding.EncodedLen(app.MaxFile)) + bodyLimit

var (
	errBody     = errors.New("runtime: sandboxd body")
	errCutShort = errors.New("runtime: sandboxd exec stream ended without an exit")
)

// Client calls sandboxd for one install. It implements app.SandboxProvider.
type Client struct {
	base string
	// http serves unary calls under unaryTimeout.
	http *http.Client
	// stream serves Exec. It has no client timeout, since a command may run
	// for the runtime cap; Exec bounds it through the context instead.
	stream *http.Client
}

var _ app.SandboxProvider = Client{}

// Open builds a client. baseURL is the install setting and serviceKey the
// sandboxd key (UPMO_SANDBOXD_KEY).
// A missing or malformed key is an error, so brain cannot hold a client that
// will never authenticate.
func Open(baseURL, serviceKey string) (Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return Client{}, fmt.Errorf("runtime: sandboxd url")
	}
	derived, err := svcsig.ParseAudienceKey(serviceKey, svcsig.AudienceSandboxd)
	if err != nil {
		return Client{}, err
	}
	transport, err := svcsig.Transport(nil, derived, svcsig.AudienceSandboxd)
	if err != nil {
		return Client{}, err
	}
	return Client{
		base:   strings.TrimRight(baseURL, "/"),
		http:   &http.Client{Transport: transport, Timeout: unaryTimeout},
		stream: &http.Client{Transport: transport},
	}, nil
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

type execBody struct {
	ID        string   `json:"id"`
	Args      []string `json:"args"`
	Dir       string   `json:"dir"`
	TimeoutMS int64    `json:"timeout_ms"`
}

// frame is one line of an Exec stream. Type is output, exit or error.
type frame struct {
	Type   string `json:"type"`
	Stream string `json:"stream"`
	Data   []byte `json:"data"`
	Code   *int   `json:"code"`
	Error  string `json:"error"`
}

type fileBody struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Data []byte `json:"data,omitempty"`
}

// Create asks sandboxd for a sandbox. Credentials travel as references and
// sentinels only.
func (c Client) Create(ctx context.Context, spec app.Spec) (app.Sandbox, error) {
	body := createBody{
		Task:        spec.Task,
		Image:       spec.Image,
		Tier:        spec.Tier,
		TTLSeconds:  int64(spec.TTL / time.Second),
		Allow:       spec.Allow,
		Credentials: make([]credentialBody, 0, len(spec.Credentials)),
		ProxySecret: spec.ProxySecret,
	}
	for _, cr := range spec.Credentials {
		body.Credentials = append(body.Credentials, credentialBody{Sentinel: cr.Sentinel, Ref: cr.Ref, Host: cr.Host, Methods: cr.Methods, Paths: cr.Paths})
	}
	var out sandboxBody
	if err := c.post(ctx, pathCreate, body, &out, bodyLimit); err != nil {
		return app.Sandbox{}, err
	}
	if out.ID == "" {
		return app.Sandbox{}, errBody
	}
	return app.Sandbox{
		ID:   out.ID,
		Tier: out.Tier,
		Shape: domain.Shape{
			Scratch: out.Shape.Scratch,
			CPU:     out.Shape.CPU,
			Burst:   out.Shape.Burst,
			PIDs:    out.Shape.PIDs,
			Exec:    time.Duration(out.Shape.ExecSeconds) * time.Second,
		},
		Expires: out.Expires,
	}, nil
}

// Pause asks sandboxd to stop the sandbox and keep its files.
func (c Client) Pause(ctx context.Context, id string) error {
	return c.post(ctx, pathPause, idBody{ID: id}, nil, bodyLimit)
}

// Destroy asks sandboxd to remove the sandbox.
func (c Client) Destroy(ctx context.Context, id string) error {
	return c.post(ctx, pathDestroy, idBody{ID: id}, nil, bodyLimit)
}

// Exec runs cmd in the sandbox and hands each output frame to out as it
// arrives. An error out returns stops the read and closes the connection,
// which cancels the command in sandboxd. The call is bounded by the
// command's timeout, or the runtime cap when it has none, plus execGrace.
func (c Client) Exec(ctx context.Context, id string, cmd app.Command, out func(app.Output) error) (app.Exit, error) {
	// The runtime cap is its own rule, so a timeout past it never travels.
	if cmd.Timeout < 0 || cmd.Timeout > domain.ResourceShape().Exec {
		return app.Exit{}, app.ErrSpec
	}
	// Whole milliseconds, rounded up, so a short timeout never reads as zero, which means the cap.
	ms := int64((cmd.Timeout + time.Millisecond - 1) / time.Millisecond)
	ctx, cancel := context.WithTimeout(ctx, execBudget(cmd.Timeout))
	defer cancel()

	res, err := c.send(ctx, c.stream, pathExec, execBody{ID: id, Args: cmd.Args, Dir: cmd.Dir, TimeoutMS: ms})
	if err != nil {
		return app.Exit{}, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, bodyLimit))
		return app.Exit{}, refusal(res)
	}
	if media, _, err := mime.ParseMediaType(res.Header.Get("Content-Type")); err != nil || media != contentTypeStream {
		return app.Exit{}, errBody
	}
	return readStream(res.Body, out)
}

// execBudget is how long an Exec call may last.
func execBudget(timeout time.Duration) time.Duration {
	limit := domain.ResourceShape().Exec
	if timeout > 0 && timeout < limit {
		limit = timeout
	}
	return limit + execGrace
}

// readStream reads frames until the exit or error frame. A line longer than
// bodyLimit, a frame it does not know or a stream with no last frame is an
// error, so a cut connection never looks like a clean exit.
func readStream(body io.Reader, out func(app.Output) error) (app.Exit, error) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), bodyLimit)
	for sc.Scan() {
		var f frame
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			return app.Exit{}, errBody
		}
		switch f.Type {
		case "output":
			if f.Stream != "stdout" && f.Stream != "stderr" {
				return app.Exit{}, errBody
			}
			if out == nil || len(f.Data) == 0 {
				continue
			}
			if err := out(app.Output{Stream: f.Stream, Data: f.Data}); err != nil {
				return app.Exit{}, err
			}
		case "exit":
			if f.Code == nil {
				return app.Exit{}, errBody
			}
			return app.Exit{Code: *f.Code}, nil
		case "error":
			if err := codeError(f.Error); err != nil {
				return app.Exit{}, err
			}
			return app.Exit{}, errors.New("runtime: sandboxd exec failed")
		default:
			return app.Exit{}, errBody
		}
	}
	if err := sc.Err(); err != nil {
		return app.Exit{}, fmt.Errorf("runtime: sandboxd: %w", err)
	}
	return app.Exit{}, errCutShort
}

// PTY is not built in sandboxd yet; the call reports app.ErrUnsupported.
func (c Client) PTY(ctx context.Context, id string, size app.Size) (io.ReadWriteCloser, error) {
	err := c.post(ctx, pathPTY, map[string]any{"id": id, "rows": size.Rows, "cols": size.Cols}, nil, bodyLimit)
	return nil, unbuilt(err)
}

// ReadFile returns a file from the sandbox. A missing path wraps
// fs.ErrNotExist, and a path that is not a regular file is app.ErrNotFile.
func (c Client) ReadFile(ctx context.Context, id, path string) ([]byte, error) {
	var out struct {
		// A pointer, so a body with no data field is refused rather than read as an empty file.
		Data *[]byte `json:"data"`
	}
	if err := c.post(ctx, pathReadFile, fileBody{ID: id, Path: path}, &out, fileLimit); err != nil {
		return nil, err
	}
	if out.Data == nil {
		return nil, errBody
	}
	if len(*out.Data) > app.MaxFile {
		return nil, app.ErrTooLarge
	}
	return *out.Data, nil
}

// WriteFile copies data to a file in the sandbox. Data over app.MaxFile is
// refused here, before it travels.
func (c Client) WriteFile(ctx context.Context, id, path string, data []byte) error {
	if len(data) > app.MaxFile {
		return app.ErrTooLarge
	}
	return c.post(ctx, pathWriteFile, fileBody{ID: id, Path: path, Data: data}, nil, bodyLimit)
}

// Expose is not built in sandboxd yet; the call reports app.ErrUnsupported.
func (c Client) Expose(ctx context.Context, id string, port int) (app.Endpoint, error) {
	err := c.post(ctx, pathExpose, map[string]any{"id": id, "port": port}, nil, bodyLimit)
	return app.Endpoint{}, unbuilt(err)
}

// StartEditor asks sandboxd to start the code editor beside the sandbox.
func (c Client) StartEditor(ctx context.Context, id, status string) (app.Endpoint, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := c.post(ctx, pathStartEditor, map[string]string{"id": id, "status": status}, &out, bodyLimit); err != nil {
		return app.Endpoint{}, err
	}
	if out.URL == "" {
		return app.Endpoint{}, errBody
	}
	return app.Endpoint{URL: out.URL}, nil
}

// StopEditor asks sandboxd to stop the code editor beside the sandbox.
func (c Client) StopEditor(ctx context.Context, id string) error {
	return c.post(ctx, pathStopEditor, idBody{ID: id}, nil, bodyLimit)
}

// unbuilt keeps a transport or refusal error and turns success, which an
// unbuilt route cannot honestly report, into app.ErrUnsupported.
func unbuilt(err error) error {
	if err == nil {
		return app.ErrUnsupported
	}
	return err
}

// post makes a unary call and decodes a 200 body of at most limit bytes
// into dest, when dest is not nil.
func (c Client) post(ctx context.Context, path string, body any, dest any, limit int64) error {
	res, err := c.send(ctx, c.http, path, body)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return fmt.Errorf("runtime: sandboxd: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return refusal(res)
	}
	if dest == nil {
		return nil
	}
	if int64(len(payload)) > limit {
		return errBody
	}
	if err := json.Unmarshal(payload, dest); err != nil {
		return errBody
	}
	return nil
}

// send posts body as JSON with hc, which signs it through the transport.
func (c Client) send(ctx context.Context, hc *http.Client, path string, body any) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("runtime: sandboxd: %w", err)
	}
	return res, nil
}

// refusal maps a non-200 response to the port's error: by its code when
// sandboxd sent one, and by status otherwise, as for a 401 from the HMAC
// guard.
func refusal(res *http.Response) error {
	if err := codeError(res.Header.Get(headerError)); err != nil {
		return err
	}
	return statusError(res.StatusCode)
}

// codeError returns the port's error for a wire code, or nil for an unknown
// code or one that names no port error.
func codeError(code string) error {
	switch code {
	case "spec":
		return app.ErrSpec
	case "isolation":
		return app.ErrIsolation
	case "not_found":
		return app.ErrNotFound
	case "unsupported":
		return app.ErrUnsupported
	case "paused":
		return app.ErrPaused
	case "too_large":
		return app.ErrTooLarge
	case "not_file":
		return app.ErrNotFile
	case "no_file":
		return fmt.Errorf("runtime: sandboxd: no such file: %w", fs.ErrNotExist)
	case "timeout":
		return fmt.Errorf("runtime: sandboxd: command timed out: %w", context.DeadlineExceeded)
	case "editor":
		return domain.ErrEditor
	default:
		return nil
	}
}

func statusError(code int) error {
	switch code {
	case http.StatusBadRequest:
		return app.ErrSpec
	case http.StatusConflict:
		return app.ErrIsolation
	case http.StatusNotFound:
		return app.ErrNotFound
	case http.StatusNotImplemented:
		return app.ErrUnsupported
	default:
		return fmt.Errorf("runtime: sandboxd %d", code)
	}
}
