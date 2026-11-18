package artifacts

import (
	"mime"
	"path/filepath"
	"strings"
)

// MaxViewBytes is the largest artifact body a text or diff view will read.
const MaxViewBytes = 256 * 1024

// kinds are the artifact kinds the catalog accepts.
var kinds = map[string]bool{
	"image":    true,
	"markdown": true,
	"csv":      true,
	"text":     true,
	"code":     true,
	"pdf":      true,
	"html":     true,
	"diff":     true,
	"binary":   true,
}

// kindByExt maps a lower-case extension to a kind. .svg is code, never image.
var kindByExt = map[string]string{
	".png":  "image",
	".jpg":  "image",
	".jpeg": "image",
	".gif":  "image",
	".webp": "image",

	".md":       "markdown",
	".markdown": "markdown",

	".csv": "csv",

	".txt": "text",
	".log": "text",

	".pdf": "pdf",

	".html": "html",
	".htm":  "html",

	".diff":  "diff",
	".patch": "diff",

	".svg":    "code",
	".ts":     "code",
	".tsx":    "code",
	".js":     "code",
	".jsx":    "code",
	".mjs":    "code",
	".cjs":    "code",
	".py":     "code",
	".go":     "code",
	".rs":     "code",
	".java":   "code",
	".kt":     "code",
	".swift":  "code",
	".rb":     "code",
	".php":    "code",
	".c":      "code",
	".h":      "code",
	".cc":     "code",
	".cpp":    "code",
	".hpp":    "code",
	".cs":     "code",
	".sql":    "code",
	".sh":     "code",
	".bash":   "code",
	".css":    "code",
	".scss":   "code",
	".json":   "code",
	".yaml":   "code",
	".yml":    "code",
	".xml":    "code",
	".toml":   "code",
	".vue":    "code",
	".svelte": "code",
}

// mediaByExt is the deterministic media type for extensions the standard
// library mislabels (notably .ts as video/mp2t) or does not know.
var mediaByExt = map[string]string{
	".md":       "text/markdown",
	".markdown": "text/markdown",
	".csv":      "text/csv",
	".json":     "application/json",
	".yaml":     "application/yaml",
	".yml":      "application/yaml",
	".ts":       "text/typescript",
	".tsx":      "text/typescript",
	".py":       "text/x-python",
	".go":       "text/x-go",
	".svg":      "image/svg+xml",
	".txt":      "text/plain",
	".log":      "text/plain",
	".html":     "text/html",
	".htm":      "text/html",
	".pdf":      "application/pdf",
	".png":      "image/png",
	".jpg":      "image/jpeg",
	".jpeg":     "image/jpeg",
	".gif":      "image/gif",
	".webp":     "image/webp",
	".diff":     "text/plain",
	".patch":    "text/plain",
}

// KindFor classifies a file name. The match is case-insensitive. A file named
// Dockerfile is code. Anything else unknown is binary.
func KindFor(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	if strings.EqualFold(base, "Dockerfile") {
		return "code"
	}
	if kind, ok := kindByExt[strings.ToLower(filepath.Ext(base))]; ok {
		return kind
	}
	return "binary"
}

// ValidKind reports whether kind is one of the nine catalog kinds.
func ValidKind(kind string) bool {
	return kinds[kind]
}

// IsTextKind reports whether a text or source view may read kind.
func IsTextKind(kind string) bool {
	switch kind {
	case "text", "markdown", "csv", "code", "html":
		return true
	default:
		return false
	}
}

// Media is the media type taken from a file name extension. An unknown or
// missing extension is application/octet-stream.
func Media(name string) string {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(name)))
	if ext == "" {
		return TypeBytes
	}
	if kind, ok := mediaByExt[ext]; ok {
		return kind
	}
	kind := mime.TypeByExtension(ext)
	if kind == "" {
		return TypeBytes
	}
	if i := strings.IndexByte(kind, ';'); i >= 0 {
		kind = kind[:i]
	}
	return strings.TrimSpace(kind)
}
