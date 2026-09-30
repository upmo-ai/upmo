// Package app holds the SandboxProvider port the runtime context owns.
// sandboxd implements it; brain reaches it only through a client
// signed with the service HMAC.
package app

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/runtime/domain"
)

var (
	// ErrSpec reports a sandbox spec that admission refuses.
	ErrSpec = errors.New("runtime: sandbox spec")
	// ErrIsolation reports a driver that cannot meet the requested tier.
	ErrIsolation = errors.New("runtime: isolation")
	// ErrNotFound reports a sandbox id the provider does not hold.
	ErrNotFound = errors.New("runtime: sandbox not found")
	// ErrUnsupported reports a provider method that is not built yet.
	ErrUnsupported = errors.New("runtime: not built")
)

// SandboxProvider creates, controls and destroys sandboxes. Every method
// names a sandbox by the id Create returned.
type SandboxProvider interface {
	// Create starts a sandbox from spec.
	Create(ctx context.Context, spec Spec) (Sandbox, error)
	// Exec runs a command and streams its output to out as it arrives.
	Exec(ctx context.Context, id string, cmd Command, out func(Output) error) (Exit, error)
	// PTY opens an interactive terminal of the given size.
	PTY(ctx context.Context, id string, size Size) (io.ReadWriteCloser, error)
	// ReadFile returns the contents of a file inside the sandbox.
	ReadFile(ctx context.Context, id, path string) ([]byte, error)
	// WriteFile copies data to a file inside the sandbox.
	WriteFile(ctx context.Context, id, path string, data []byte) error
	// Expose publishes a port inside the sandbox and returns where it is reached.
	Expose(ctx context.Context, id string, port int) (Endpoint, error)
	// Pause stops the sandbox and keeps its files.
	Pause(ctx context.Context, id string) error
	// Destroy removes the sandbox and everything in it.
	Destroy(ctx context.Context, id string) error
}

// Spec describes one sandbox. It never carries a credential value, only
// references and the sentinels the sandbox sees in their place.
type Spec struct {
	Task        string
	Image       string
	Tier        string
	TTL         time.Duration
	Allow       []string
	Credentials []Credential
}

// Credential maps a sentinel the sandbox sees to a reference sandboxd
// resolves, injected only toward Host.
type Credential struct {
	Sentinel string
	Ref      string
	Host     string
}

// Sandbox is a created sandbox.
type Sandbox struct {
	ID      string
	Tier    string
	Shape   domain.Shape
	Expires time.Time
}

// Command is one command to run inside a sandbox.
type Command struct {
	Args    []string
	Dir     string
	Timeout time.Duration
}

// Output is one chunk of a command's output. Stream is "stdout" or "stderr".
type Output struct {
	Stream string
	Data   []byte
}

// Exit is how a command ended.
type Exit struct {
	Code int
}

// Size is a terminal size in character cells.
type Size struct {
	Rows int
	Cols int
}

// Endpoint is where an exposed port is reached.
type Endpoint struct {
	URL string
}

// CheckSpec returns spec normalised, or ErrSpec when admission refuses it.
// Allow entries follow the egress rules of the domain, and every credential
// is injected only toward a host on the allow-list.
func CheckSpec(spec Spec) (Spec, error) {
	task, err := uuid.Parse(strings.TrimSpace(spec.Task))
	if err != nil || task == uuid.Nil {
		return Spec{}, ErrSpec
	}
	image := strings.TrimSpace(spec.Image)
	if image == "" || strings.ContainsAny(image, " \t\r\n") {
		return Spec{}, ErrSpec
	}
	tier := strings.ToLower(strings.TrimSpace(spec.Tier))
	switch tier {
	case domain.TierContainer, domain.TierGVisor, domain.TierKata:
	default:
		return Spec{}, ErrSpec
	}
	if spec.TTL <= 0 {
		return Spec{}, ErrSpec
	}

	allow := make([]string, 0, len(spec.Allow))
	for _, entry := range spec.Allow {
		host, ok := allowEntry(entry)
		if !ok {
			return Spec{}, ErrSpec
		}
		if !slices.Contains(allow, host) {
			allow = append(allow, host)
		}
	}

	creds := make([]Credential, 0, len(spec.Credentials))
	seen := map[string]bool{}
	for _, c := range spec.Credentials {
		sentinel, ref := strings.TrimSpace(c.Sentinel), strings.TrimSpace(c.Ref)
		host, ok := allowEntry(c.Host)
		switch {
		case !ok, !slices.Contains(allow, host),
			sentinel == "", ref == "", sentinel == ref, seen[sentinel],
			strings.ContainsAny(sentinel+ref, " \t\r\n"):
			return Spec{}, ErrSpec
		}
		seen[sentinel] = true
		creds = append(creds, Credential{Sentinel: sentinel, Ref: ref, Host: host})
	}

	return Spec{Task: task.String(), Image: image, Tier: tier, TTL: spec.TTL, Allow: allow, Credentials: creds}, nil
}

// allowEntry normalises a host name through the domain egress rule: a listed
// name with no port, never an address or a name that means the host.
func allowEntry(entry string) (string, bool) {
	d, err := domain.AdmitEgress(entry, []string{entry})
	if err != nil || d.Verdict != domain.VerdictAllow || d.Port != "" {
		return "", false
	}
	return d.Host, true
}
