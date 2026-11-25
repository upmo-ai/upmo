package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"encoding/base64"

	"github.com/upmo-ai/upmo/internal/platform/editorjwt"
	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

type fakeEditorBoxes struct {
	endpoint runtime.Endpoint
	err      error
	calls    int
	lastID   string
	status   string
}

func (f *fakeEditorBoxes) StartEditor(_ context.Context, taskID, status string) (runtime.Endpoint, error) {
	f.calls++
	f.lastID = taskID
	f.status = status
	return f.endpoint, f.err
}

type fakeEditorSigner struct {
	token string
	err   error
}

func (f fakeEditorSigner) Sign(_, _, _ string, _ time.Time) (string, error) {
	return f.token, f.err
}

func TestOpenEditorReturnsURLAndToken(t *testing.T) {
	view := &fakeViewer{}
	boxes := &fakeEditorBoxes{endpoint: runtime.Endpoint{URL: "http://127.0.0.1:30001"}}
	now := time.Unix(1_700_000_000, 0)
	got, err := OpenEditor(
		context.Background(),
		view,
		boxes,
		fakeEditorSigner{token: "signed"},
		"http://localhost:5173",
		Access{UserID: userID, Allow: "task:view"},
		preparingTask,
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "http://127.0.0.1:30001" || got.Token != "signed" || !got.ExpiresAt.Equal(now.UTC().Add(runtime.TokenTTL)) {
		t.Fatalf("session = %+v", got)
	}
	if boxes.calls != 1 || boxes.lastID != preparingTask || boxes.status != string(domain.Preparing) {
		t.Fatalf("boxes = %+v", boxes)
	}
}

func TestOpenEditorNeedsHeldSandbox(t *testing.T) {
	view := &fakeViewer{}
	boxes := &fakeEditorBoxes{err: runtime.ErrNoHeldSandbox}
	_, err := OpenEditor(context.Background(), view, boxes, fakeEditorSigner{token: "x"}, "http://localhost:5173", Access{UserID: userID, Allow: "task:view"}, preparingTask, time.Now())
	if !errors.Is(err, ErrNoTaskSandbox) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenEditorRefusesSharedOrigin(t *testing.T) {
	view := &fakeViewer{}
	boxes := &fakeEditorBoxes{endpoint: runtime.Endpoint{URL: "http://localhost:5173"}}
	_, err := OpenEditor(context.Background(), view, boxes, fakeEditorSigner{token: "x"}, "http://localhost:5173", Access{UserID: userID, Allow: "task:view"}, preparingTask, time.Now())
	if !errors.Is(err, runtime.ErrEditor) {
		t.Fatalf("err = %v", err)
	}
}

func TestNewEditorTokensSigns(t *testing.T) {
	raw := bytes32('k')
	key := editorjwt.KeyPrefix + base64.RawStdEncoding.EncodeToString(raw)
	tokens, err := NewEditorTokens(key, "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	user := "11111111-1111-1111-1111-111111111111"
	task := "22222222-2222-2222-2222-222222222222"
	session := "33333333-3333-3333-3333-333333333333"
	token, err := tokens.Sign(user, task, session, now)
	if err != nil {
		t.Fatal(err)
	}
	k, err := editorjwt.New([][]byte{raw})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := k.Verify(token, now)
	if err != nil {
		t.Fatal(err)
	}
	if claims.User != user || claims.Task != task || claims.Session != session {
		t.Fatalf("claims = %+v", claims)
	}
}

func bytes32(b byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = b
	}
	return out
}
