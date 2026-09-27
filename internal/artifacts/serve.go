package artifacts

import "strings"

// The headers an artifact is served with, and the values they may take.
const (
	HeaderType        = "Content-Type"
	HeaderDisposition = "Content-Disposition"
	HeaderSniff       = "X-Content-Type-Options"
	SniffValue        = "nosniff"
	TypeBytes         = "application/octet-stream"
)

// Headers are the values a caller sets when it serves an artifact.
type Headers struct {
	Type        string
	Disposition string
	Sniff       string
}

// inline lists the types a browser may show in place. Every other type is
// sent as an attachment.
var inline = map[string]bool{
	"image/png":       true,
	"image/jpeg":      true,
	"image/gif":       true,
	"image/webp":      true,
	"application/pdf": true,
	"text/plain":      true,
	"text/csv":        true,
}

// markup lists the types that could run script on the app origin. They are
// never emitted; an artifact claiming one is sent as plain bytes.
var markup = map[string]bool{
	"text/html":             true,
	"application/xhtml+xml": true,
	"image/svg+xml":         true,
}

// Serve works out the headers for an artifact with the given name and media
// type. A name or media value that could split or escape a header is
// ErrArtifact.
func Serve(name, media string) (Headers, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "\r\n\"\\") {
		return Headers{}, ErrArtifact
	}
	if strings.ContainsAny(media, "\r\n") {
		return Headers{}, ErrArtifact
	}

	kind := normalise(media)
	disposition := "attachment"
	switch {
	case !wellFormed(kind), markup[kind]:
		kind = TypeBytes
	case inline[kind]:
		disposition = "inline"
	}

	return Headers{
		Type:        kind,
		Disposition: disposition + `; filename="` + name + `"`,
		Sniff:       SniffValue,
	}, nil
}

// normalise trims and lowercases a media value and drops its parameters.
func normalise(media string) string {
	media = strings.ToLower(strings.TrimSpace(media))
	if i := strings.IndexByte(media, ';'); i >= 0 {
		media = media[:i]
	}
	return strings.TrimSpace(media)
}

// wellFormed reports whether kind is a type and a subtype, each made only of
// letters, digits, and ! # $ & ^ _ . + -.
func wellFormed(kind string) bool {
	typ, sub, ok := strings.Cut(kind, "/")
	return ok && token(typ) && token(sub)
}

func token(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$&^_.+-", c) >= 0:
		default:
			return false
		}
	}
	return true
}
