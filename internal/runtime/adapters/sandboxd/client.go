// Package sandboxd is brain's client for the sandboxd process. It signs each
// call with the service HMAC for the sandboxd audience and does
// not import the sandboxd process or any container engine.
package sandboxd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/runtime/app"
	"github.com/upmo-ai/upmo/internal/runtime/domain"
)

const (
	pathCreate    = "/upmo.sandboxd.v1.Runtime/Create"
	pathExec      = "/upmo.sandboxd.v1.Runtime/Exec"
	pathPTY       = "/upmo.sandboxd.v1.Runtime/PTY"
	pathReadFile  = "/upmo.sandboxd.v1.Runtime/ReadFile"
	pathWriteFile = "/upmo.sandboxd.v1.Runtime/WriteFile"
	pathExpose    = "/upmo.sandboxd.v1.Runtime/Expose"
	pathPause     = "/upmo.sandboxd.v1.Runtime/Pause"
	pathDestroy   = "/upmo.sandboxd.v1.Runtime/Destroy"
)

const bodyLimit = 1 << 20

// Client calls sandboxd for one install. It implements app.SandboxProvider.
type Client struct {
	base string
	http *http.Client
}

var _ app.SandboxProvider = Client{}

// Open builds a client. baseURL and serviceKey are the install settings.
// A missing or malformed key is an error, so brain cannot hold a client that
// will never authenticate.
func Open(baseURL, serviceKey string) (Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return Client{}, fmt.Errorf("runtime: sandboxd url")
	}
	master, err := svcsig.ParseKey(serviceKey)
	if err != nil {
		return Client{}, err
	}
	derived, err := svcsig.Derive(master, svcsig.AudienceSandboxd)
	if err != nil {
		return Client{}, err
	}
	transport, err := svcsig.Transport(nil, derived, svcsig.AudienceSandboxd)
	if err != nil {
		return Client{}, err
	}
	return Client{
		base: strings.TrimRight(baseURL, "/"),
		http: &http.Client{Transport: transport, Timeout: 30 * time.Second},
	}, nil
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
	}
	for _, cr := range spec.Credentials {
		body.Credentials = append(body.Credentials, credentialBody{Sentinel: cr.Sentinel, Ref: cr.Ref, Host: cr.Host})
	}
	var out sandboxBody
	if err := c.post(ctx, pathCreate, body, &out); err != nil {
		return app.Sandbox{}, err
	}
	if out.ID == "" {
		return app.Sandbox{}, fmt.Errorf("runtime: sandboxd body")
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
	return c.post(ctx, pathPause, idBody{ID: id}, nil)
}

// Destroy asks sandboxd to remove the sandbox.
func (c Client) Destroy(ctx context.Context, id string) error {
	return c.post(ctx, pathDestroy, idBody{ID: id}, nil)
}

// Exec is not built in sandboxd yet; the call reports app.ErrUnsupported.
func (c Client) Exec(ctx context.Context, id string, cmd app.Command, _ func(app.Output) error) (app.Exit, error) {
	err := c.post(ctx, pathExec, map[string]any{"id": id, "args": cmd.Args, "dir": cmd.Dir}, nil)
	return app.Exit{}, unbuilt(err)
}

// PTY is not built in sandboxd yet; the call reports app.ErrUnsupported.
func (c Client) PTY(ctx context.Context, id string, size app.Size) (io.ReadWriteCloser, error) {
	err := c.post(ctx, pathPTY, map[string]any{"id": id, "rows": size.Rows, "cols": size.Cols}, nil)
	return nil, unbuilt(err)
}

// ReadFile is not built in sandboxd yet; the call reports app.ErrUnsupported.
func (c Client) ReadFile(ctx context.Context, id, path string) ([]byte, error) {
	err := c.post(ctx, pathReadFile, map[string]any{"id": id, "path": path}, nil)
	return nil, unbuilt(err)
}

// WriteFile is not built in sandboxd yet; the call reports app.ErrUnsupported.
func (c Client) WriteFile(ctx context.Context, id, path string, data []byte) error {
	err := c.post(ctx, pathWriteFile, map[string]any{"id": id, "path": path, "data": data}, nil)
	return unbuilt(err)
}

// Expose is not built in sandboxd yet; the call reports app.ErrUnsupported.
func (c Client) Expose(ctx context.Context, id string, port int) (app.Endpoint, error) {
	err := c.post(ctx, pathExpose, map[string]any{"id": id, "port": port}, nil)
	return app.Endpoint{}, unbuilt(err)
}

// unbuilt keeps a transport or refusal error and turns success, which an
// unbuilt route cannot honestly report, into app.ErrUnsupported.
func unbuilt(err error) error {
	if err == nil {
		return app.ErrUnsupported
	}
	return err
}

func (c Client) post(ctx context.Context, path string, body any, dest any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("runtime: sandboxd: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(res.Body, bodyLimit))
	if err != nil {
		return fmt.Errorf("runtime: sandboxd: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return statusError(res.StatusCode)
	}
	if dest == nil {
		return nil
	}
	if err := json.Unmarshal(payload, dest); err != nil {
		return errors.New("runtime: sandboxd body")
	}
	return nil
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
