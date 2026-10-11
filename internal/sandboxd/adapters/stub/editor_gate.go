package stub

import (
	"context"

	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/sandboxd/editorgate"
)

// EditorRoute is not built in the stub driver.
func (d *Driver) EditorRoute(context.Context, string) (editorgate.Route, error) {
	return editorgate.Route{}, runtime.ErrNotFound
}

// TouchEditor is not built in the stub driver.
func (d *Driver) TouchEditor(context.Context, string) error {
	return runtime.ErrNotFound
}
