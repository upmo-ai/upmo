// Package runtime is the runtime context facade. It owns the SandboxProvider
// port; sandboxd implements it and brain calls it through Open.
package runtime

import (
	"github.com/upmo-ai/upmo/internal/runtime/adapters/sandboxd"
	"github.com/upmo-ai/upmo/internal/runtime/app"
	"github.com/upmo-ai/upmo/internal/runtime/domain"
)

// The port and its messages.
type (
	SandboxProvider = app.SandboxProvider
	Spec            = app.Spec
	Credential      = app.Credential
	Sandbox         = app.Sandbox
	Command         = app.Command
	Output          = app.Output
	Exit            = app.Exit
	Size            = app.Size
	Endpoint        = app.Endpoint
	Shape           = domain.Shape
)

// Seats holds the sandbox each task occupies, in process memory. Class is
// a tool class NeedsSandbox reads.
type (
	Seats = app.Seats
	Class = domain.Class
)

// Tool classes.
const (
	ClassShell     = domain.ClassShell
	ClassFile      = domain.ClassFile
	ClassConnector = domain.ClassConnector
	ClassKnowledge = domain.ClassKnowledge
	ClassWeb       = domain.ClassWeb
)

// Seat defaults.
const (
	SeatImage = app.SeatImage
	SeatTTL   = app.SeatTTL
)

// Errors Seats reports.
var (
	ErrSandboxdUnset = app.ErrSandboxdUnset
	ErrSeated        = app.ErrSeated
	ErrReleased      = app.ErrReleased
	ErrClass         = domain.ErrClass
)

// NeedsSandbox reports whether a task with repo and classes needs a
// sandbox, or ErrClass for an empty or unknown class.
func NeedsSandbox(repo string, classes []Class) (bool, error) {
	return domain.NeedsSandbox(repo, classes)
}

// Egress decisions, so sandboxd applies the rules the domain owns.
type (
	Decision = domain.Decision
	Request  = domain.Request
)

// Errors a provider reports.
var (
	ErrSpec        = app.ErrSpec
	ErrIsolation   = app.ErrIsolation
	ErrNotFound    = app.ErrNotFound
	ErrUnsupported = app.ErrUnsupported
	ErrEgress      = domain.ErrEgress
)

// Tiers and verdicts.
const (
	TierContainer = domain.TierContainer
	TierGVisor    = domain.TierGVisor
	TierKata      = domain.TierKata
	VerdictAllow  = domain.VerdictAllow
	VerdictRefuse = domain.VerdictRefuse
)

// CheckSpec returns spec normalised, or ErrSpec when admission refuses it.
func CheckSpec(spec Spec) (Spec, error) { return app.CheckSpec(spec) }

// ResourceShape returns the one shape every sandbox gets.
func ResourceShape() Shape { return domain.ResourceShape() }

// AdmitEgress decides whether a sandbox may reach host, given its allow-list.
func AdmitEgress(host string, allow []string) (Decision, error) {
	return domain.AdmitEgress(host, allow)
}

// Note copies a decision into a log record after checking it is well formed.
func Note(d Decision) (Request, error) { return domain.Note(d) }

// Open returns brain's SandboxProvider: a client for sandboxd at baseURL that
// signs every call with the service key for the sandboxd audience.
func Open(baseURL, serviceKey string) (SandboxProvider, error) {
	c, err := sandboxd.Open(baseURL, serviceKey)
	if err != nil {
		return nil, err
	}
	return c, nil
}
