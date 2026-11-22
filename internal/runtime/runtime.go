// Package runtime is the runtime context facade. It owns the SandboxProvider
// port; sandboxd implements it and brain calls it through Open.
// It embeds this context's SQL, which records the sandbox each task holds.
package runtime

import (
	"embed"
	"io/fs"
	"time"

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

// NewTaskSandboxStore returns the TaskSandboxStore kept in the runtime schema, so a
// task sandbox outlives the brain process that created it.
func NewTaskSandboxStore(pool *pgxpool.Pool) TaskSandboxStore {
	return postgres.TaskSandboxes{Pool: pool}
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
	Placement       = domain.Placement
)

// TaskSandboxes holds the sandbox each task occupies and records it in a
// TaskSandboxStore. Done receives the outcome of the provisioning
// TaskSandboxes.Provision runs in the background. Class is a tool class
// NeedsSandbox reads.
type (
	TaskSandboxes    = app.TaskSandboxes
	TaskSandboxStore = app.TaskSandboxStore
	Done             = app.Done
	Class            = domain.Class
)

// Tool classes.
const (
	ClassShell     = domain.ClassShell
	ClassFile      = domain.ClassFile
	ClassConnector = domain.ClassConnector
	ClassKnowledge = domain.ClassKnowledge
	ClassWeb       = domain.ClassWeb
)

// Sandbox defaults.
const (
	SandboxImage = app.SandboxImage
	SandboxTTL   = app.SandboxTTL
)

// Errors TaskSandboxes reports.
var (
	ErrSandboxdUnset      = app.ErrSandboxdUnset
	ErrNoTaskSandboxStore = app.ErrNoTaskSandboxStore
	ErrAlreadyHeld        = app.ErrAlreadyHeld
	ErrNoHeldSandbox      = app.ErrNoHeldSandbox
	ErrReleased           = app.ErrReleased
	ErrClass              = domain.ErrClass
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
// each step as its own event. TaskSandboxes.Provision runs it in the
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
	ErrEditor      = domain.ErrEditor
)

// The code editor beside a sandbox: its scratch mount, idle clock, egress
// identity and token.
type (
	Mount = domain.Mount
	Token = domain.Token
)

// Editor modes, limits and labels.
const (
	ModeReadOnly       = domain.ModeReadOnly
	EditorIdle         = domain.EditorIdle
	EditorTouchedLabel = domain.EditorTouchedLabel
	GalleryHost        = domain.GalleryHost
	TokenTTL           = domain.TokenTTL
)

// MountRule returns the scratch mount for a task status.
func MountRule(status string) (Mount, error) { return domain.MountRule(status) }

// FormatEditorTouched formats now as the editor idle clock label value.
func FormatEditorTouched(now time.Time) string { return domain.FormatEditorTouched(now) }

// StopIdleEditor reports whether an editor with touched should stop at now.
func StopIdleEditor(touched string, now time.Time) (bool, error) {
	return domain.StopIdleEditor(touched, now)
}

// AdmitEditor refuses another editor when a task already holds open editors at the cap.
func AdmitEditor(open int) error { return domain.AdmitEditor(open) }

// EditorEgressID is the CONNECT proxy Basic user for a sandbox code editor.
func EditorEgressID(sandboxID string) string { return domain.EditorEgressID(sandboxID) }

// SandboxFromEditorEgress returns the sandbox id when id is an editor egress id.
func SandboxFromEditorEgress(id string) (string, bool) { return domain.SandboxFromEditorEgress(id) }

// Issue returns an unsigned editor token for user, task and session that
// expires TokenTTL after now.
func Issue(user, task, session string, now time.Time) (Token, error) {
	return domain.Issue(user, task, session, now)
}

// SeparateOrigin refuses an editor URL that shares an origin with the app URL.
func SeparateOrigin(app, editor string) error { return domain.SeparateOrigin(app, editor) }

// MaxFile is the largest file ReadFile returns or WriteFile copies.
const MaxFile = app.MaxFile

// Tiers, platforms, runtimes and verdicts.
const (
	TierContainer      = domain.TierContainer
	TierGVisor         = domain.TierGVisor
	TierKata           = domain.TierKata
	PlatformCompose    = domain.PlatformCompose
	PlatformKubernetes = domain.PlatformKubernetes
	RuntimeRunsc       = domain.RuntimeRunsc
	RuntimeClassGVisor = domain.RuntimeClassGVisor
	RuntimeClassKata   = domain.RuntimeClassKata
	VerdictAllow       = domain.VerdictAllow
	VerdictRefuse      = domain.VerdictRefuse
)

// ErrTier reports an unknown tier or platform, or a platform that cannot
// meet the tier.
var ErrTier = domain.ErrTier

// Place returns a placement that meets tier on platform, and never names a
// weaker runtime.
func Place(tier, platform string) (Placement, error) {
	return domain.Place(tier, platform)
}

// CheckSpec returns spec normalized, or ErrSpec when admission refuses it.
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
