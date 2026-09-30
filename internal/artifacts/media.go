package artifacts

import (
	"mime"
	"path/filepath"
	"strings"
)

// Media is the media type taken from a file name extension. An unknown or
// missing extension is application/octet-stream.
func Media(name string) string {
	ext := filepath.Ext(strings.TrimSpace(name))
	if ext == "" {
		return TypeBytes
	}
	kind := mime.TypeByExtension(strings.ToLower(ext))
	if kind == "" {
		return TypeBytes
	}
	if i := strings.IndexByte(kind, ';'); i >= 0 {
		kind = kind[:i]
	}
	return strings.TrimSpace(kind)
}
