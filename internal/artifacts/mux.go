package artifacts

import (
	"context"
	"io"
)

// Mux dispatches Blobs.Open by store kind.
type Mux struct {
	Volume Blobs
	Object Blobs
}

// Open routes volume keys to Volume and object keys to Object.
func (m Mux) Open(ctx context.Context, store, key string) (io.ReadCloser, error) {
	switch store {
	case StoreVolume:
		if m.Volume == nil {
			return nil, ErrNoBlob
		}
		return m.Volume.Open(ctx, store, key)
	case StoreObject:
		if m.Object == nil {
			return nil, ErrBlob
		}
		return m.Object.Open(ctx, store, key)
	default:
		return nil, ErrBlob
	}
}
