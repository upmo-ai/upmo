package artifacts_test

import (
	"testing"

	"github.com/upmo-ai/upmo/internal/artifacts"
)

func TestKindFor_UPM222_Classification(t *testing.T) {
	cases := []struct {
		name, kind string
	}{
		{"photo.png", "image"},
		{"photo.JPG", "image"},
		{"photo.jpeg", "image"},
		{"anim.GIF", "image"},
		{"anim.webp", "image"},
		{"notes.md", "markdown"},
		{"notes.MARKDOWN", "markdown"},
		{"data.csv", "csv"},
		{"notes.TXT", "text"},
		{"run.log", "text"},
		{"main.go", "code"},
		{"app.ts", "code"},
		{"app.TSX", "code"},
		{"app.py", "code"},
		{"app.json", "code"},
		{"app.yaml", "code"},
		{"app.yml", "code"},
		{"icon.svg", "code"},
		{"icon.SVG", "code"},
		{"Dockerfile", "code"},
		{"dockerfile", "code"},
		{"dir/Dockerfile", "code"},
		{"doc.pdf", "pdf"},
		{"page.html", "html"},
		{"page.HTM", "html"},
		{"changes.diff", "diff"},
		{"changes.patch", "diff"},
		{"blob.bin", "binary"},
		{"noext", "binary"},
	}
	for _, c := range cases {
		if got := artifacts.KindFor(c.name); got != c.kind {
			t.Errorf("KindFor(%q) = %q, want %q", c.name, got, c.kind)
		}
	}
	if got := artifacts.KindFor("icon.svg"); got == "image" {
		t.Fatal("svg classified as image")
	}
	if got := artifacts.KindFor("app.ts"); got == "binary" {
		t.Fatal("ts classified as binary")
	}

	for _, kind := range []string{"image", "markdown", "csv", "text", "code", "pdf", "html", "diff", "binary"} {
		if !artifacts.ValidKind(kind) {
			t.Errorf("ValidKind(%q) = false", kind)
		}
	}
	for _, kind := range []string{"", "Image", "video", "video/mp2t"} {
		if artifacts.ValidKind(kind) {
			t.Errorf("ValidKind(%q) = true", kind)
		}
	}

	for _, kind := range []string{"text", "markdown", "csv", "code", "html"} {
		if !artifacts.IsTextKind(kind) {
			t.Errorf("IsTextKind(%q) = false", kind)
		}
	}
	for _, kind := range []string{"image", "pdf", "diff", "binary", ""} {
		if artifacts.IsTextKind(kind) {
			t.Errorf("IsTextKind(%q) = true", kind)
		}
	}
}

func TestMedia_UPM222_ExtensionMapping(t *testing.T) {
	cases := []struct {
		name, media string
	}{
		{"notes.md", "text/markdown"},
		{"notes.markdown", "text/markdown"},
		{"data.csv", "text/csv"},
		{"app.json", "application/json"},
		{"app.yaml", "application/yaml"},
		{"app.yml", "application/yaml"},
		{"app.ts", "text/typescript"},
		{"app.TS", "text/typescript"},
		{"app.tsx", "text/typescript"},
		{"app.py", "text/x-python"},
		{"main.go", "text/x-go"},
		{"icon.svg", "image/svg+xml"},
		{"notes.txt", "text/plain"},
		{"page.html", "text/html"},
		{"doc.pdf", "application/pdf"},
	}
	for _, c := range cases {
		if got := artifacts.Media(c.name); got != c.media {
			t.Errorf("Media(%q) = %q, want %q", c.name, got, c.media)
		}
	}
	if got := artifacts.Media("app.ts"); got == "video/mp2t" {
		t.Fatal("ts mapped to video/mp2t")
	}
	if got := artifacts.KindFor("icon.svg"); got != "code" {
		t.Fatalf("svg kind = %q", got)
	}
}
