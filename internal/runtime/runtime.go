// Package runtime is the runtime context facade. It owns the SandboxProvider
// port; sandboxd implements it and brain calls it through Open.
// It embeds this context's SQL, which records the seat each task holds.
package runtime

import (
	"embed"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/runtime/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/runtime/adapters/sandboxd"
	"github.com/upmo-ai/upmo/internal/runtime/app"
	"github.com/upmo-ai/upmo/internal/runtime/domain"
)

// Schema is the Postgres schema this context owns.
const Schema = "runtime"

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns this context's migration files. A runner applies them
// and nothing from any other context.
func Migrations() fs.FS {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic("runtime: embed migrations: " + err.Error())
	}
	return files
}

// SeatStore returns the SeatBook kept in the runtime schema, so a seat
// outlives the brain process that created it.
func SeatStore(pool *pgxpool.Pool) SeatBook {
	return postgres.Seats{Pool: pool}
}

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

// Seats holds the sandbox each task occupies and records it in a SeatBook.
// Done receives the outcome of the provisioning Seats.Provision runs in the
// background. Class is a tool class NeedsSandbox reads.
type (
	Seats    = app.Seats
	SeatBook = app.SeatBook
	Done     = app.Done
	Class    = domain.Class
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
	ErrNoSeatBook    = app.ErrNoSeatBook
	ErrSeated        = app.ErrSeated
	ErrReleased      = app.ErrReleased
	ErrClass         = domain.ErrClass
)

// NeedsSandbox reports whether a task with repo and classes needs a
// sandbox, or ErrClass for an empty or unknown class.
func NeedsSandbox(repo string, classes []Class) (bool, error) {
	return domain.NeedsSandbox(repo, classes)
}

// CloneURL returns the URL a repository is cloned from, empty for an empty
// repository, or ErrRepo for one that cannot be cloned.
func CloneURL(repo string) (string, error) { return domain.CloneURL(repo) }

// Provisioning runs clone, toolchain and probe in a new sandbox and records
// each step as its own event. Seats.Provision runs it in the
// background after Create; a PhaseError names the phase that failed.
type (
	Provisioner = app.Provisioner
	Record      = domain.Record
	Phase       = domain.Phase
	PhaseError  = app.PhaseError
)

// Provisioning phases.
const (
	PhaseClone     = domain.PhaseClone
	PhaseToolchain = domain.PhaseToolchain
	PhaseProbe     = domain.PhaseProbe
	PhaseReady     = domain.PhaseReady
	PhaseFailed    = domain.PhaseFailed
)

// Errors provisioning reports.
var (
	ErrProvisionFailed = app.ErrProvisionFailed
	ErrCommand         = app.ErrCommand
	ErrRepo            = domain.ErrRepo
)

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
	ErrPaused      = app.ErrPaused
	ErrTooLarge    = app.ErrTooLarge
	ErrNotFile     = app.ErrNotFile
	ErrEgress      = domain.ErrEgress
)

// MaxFile is the largest file ReadFile returns or WriteFile copies.
const MaxFile = app.MaxFile

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
