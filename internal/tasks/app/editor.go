package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/platform/editorjwt"
	"github.com/upmo-ai/upmo/internal/runtime"
	runtimedomain "github.com/upmo-ai/upmo/internal/runtime/domain"
)

// ErrNoTaskSandbox is a task that needs an editor but holds no sandbox.
var ErrNoTaskSandbox = fmt.Errorf("%w: task holds no sandbox", ErrSandbox)

// EditorSession opens the code editor beside a task sandbox.
type EditorSession struct {
	URL       string
	Token     string
	ExpiresAt time.Time
}

// EditorSigner mints a bound editor JWT for one person, task and session.
type EditorSigner interface {
	Sign(user, task, session string, now time.Time) (string, error)
}

// EditorSandboxes starts the editor beside a held task sandbox.
type EditorSandboxes interface {
	StartEditor(ctx context.Context, taskID, status string) (runtime.Endpoint, error)
}

// EditorTokens mints editor JWTs from the service master key.
type EditorTokens struct {
	keys editorjwt.Keys
}

// NewEditorTokens derives signing keys from the current and optional previous master.
func NewEditorTokens(current, previous string) (EditorTokens, error) {
	if current == "" {
		return EditorTokens{}, editorjwt.ErrNoKey
	}
	var derived [][]byte
	for _, secret := range []string{current, previous} {
		if secret == "" {
			continue
		}
		master, err := editorjwt.ParseKey(secret)
		if err != nil {
			return EditorTokens{}, err
		}
		key, err := editorjwt.Derive(master)
		if err != nil {
			return EditorTokens{}, err
		}
		derived = append(derived, key)
	}
	keys, err := editorjwt.New(derived)
	if err != nil {
		return EditorTokens{}, err
	}
	return EditorTokens{keys: keys}, nil
}

// Sign returns a 15-minute editor JWT.
func (t EditorTokens) Sign(user, task, session string, now time.Time) (string, error) {
	return t.keys.Sign(user, task, session, now)
}

// OpenEditor views the task, starts the editor beside its sandbox, and returns
// a separate-origin URL with a short-lived bound token.
func OpenEditor(ctx context.Context, view Viewer, boxes EditorSandboxes, signer EditorSigner, publicURL string, access Access, taskID string, now time.Time) (EditorSession, error) {
	task, err := ViewTask(ctx, view, access, taskID)
	if err != nil {
		return EditorSession{}, err
	}
	if boxes == nil {
		return EditorSession{}, ErrNoTaskSandbox
	}
	ep, err := boxes.StartEditor(ctx, task.ID, string(task.Status))
	if err != nil {
		return EditorSession{}, mapEditorStart(err)
	}
	sessionID := uuid.New().String()
	token, err := runtimedomain.Issue(access.UserID, task.ID, sessionID, now)
	if err != nil {
		return EditorSession{}, err
	}
	if signer == nil {
		return EditorSession{}, editorjwt.ErrNoKey
	}
	signed, err := signer.Sign(token.User, token.Task, token.Session, now)
	if err != nil {
		return EditorSession{}, err
	}
	if err := runtimedomain.SeparateOrigin(publicURL, ep.URL); err != nil {
		return EditorSession{}, err
	}
	return EditorSession{URL: ep.URL, Token: signed, ExpiresAt: token.Expires}, nil
}

func mapEditorStart(err error) error {
	switch {
	case errors.Is(err, runtime.ErrNoHeldSandbox):
		return ErrNoTaskSandbox
	case errors.Is(err, runtime.ErrSandboxdUnset):
		return ErrSandboxUnset
	default:
		return err
	}
}
