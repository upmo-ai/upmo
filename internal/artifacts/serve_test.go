package artifacts_test

import (
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/artifacts"
)

func TestServeInline(t *testing.T) {
	cases := []struct {
		name, media, wantType string
	}{
		{"report.pdf", "application/pdf", "application/pdf"},
		{"photo.png", " image/PNG ; charset=binary ", "image/png"},
		{"notes.txt", "text/plain", "text/plain"},
		{"data.csv", "text/csv", "text/csv"},
	}
	for _, c := range cases {
		got, err := artifacts.Serve(c.name, c.media)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		want := artifacts.Headers{
			Type:        c.wantType,
			Disposition: `inline; filename="` + c.name + `"`,
			Sniff:       artifacts.SniffValue,
		}
		if got != want {
			t.Fatalf("%s: headers = %#v, want %#v", c.name, got, want)
		}
	}
}

func TestServeReportPDF(t *testing.T) {
	got, err := artifacts.Serve("report.pdf", "application/pdf")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "application/pdf" {
		t.Fatalf("type = %q", got.Type)
	}
	if got.Sniff != "nosniff" {
		t.Fatalf("sniff = %q", got.Sniff)
	}
	if got.Disposition != `inline; filename="report.pdf"` {
		t.Fatalf("disposition = %q", got.Disposition)
	}
}

func TestServeNeverSendsMarkup(t *testing.T) {
	for _, media := range []string{
		"text/html",
		"TEXT/HTML",
		"text/html; charset=utf-8",
		"application/xhtml+xml",
		"image/svg+xml",
	} {
		got, err := artifacts.Serve("page.html", media)
		if err != nil {
			t.Fatalf("%q: %v", media, err)
		}
		if got.Type != artifacts.TypeBytes {
			t.Fatalf("%q: type = %q, want %q", media, got.Type, artifacts.TypeBytes)
		}
		if got.Type == "text/html" {
			t.Fatalf("%q: type is text/html", media)
		}
		if got.Disposition != `attachment; filename="page.html"` {
			t.Fatalf("%q: disposition = %q", media, got.Disposition)
		}
		if got.Sniff != artifacts.SniffValue {
			t.Fatalf("%q: sniff = %q", media, got.Sniff)
		}
	}
}

func TestServeAttachment(t *testing.T) {
	cases := []struct {
		name, media, wantType string
	}{
		{"archive.zip", "application/zip", "application/zip"},
		{"blank.bin", "", artifacts.TypeBytes},
		{"blank.bin", "   ", artifacts.TypeBytes},
		{"photo.jpg", "image/jpg", "image/jpg"},
		{"odd.bin", "notatype", artifacts.TypeBytes},
		{"odd.bin", "image/", artifacts.TypeBytes},
		{"odd.bin", "a b/c", artifacts.TypeBytes},
	}
	for _, c := range cases {
		got, err := artifacts.Serve(c.name, c.media)
		if err != nil {
			t.Fatalf("%q: %v", c.media, err)
		}
		want := artifacts.Headers{
			Type:        c.wantType,
			Disposition: `attachment; filename="` + c.name + `"`,
			Sniff:       artifacts.SniffValue,
		}
		if got != want {
			t.Fatalf("%q: headers = %#v, want %#v", c.media, got, want)
		}
	}
}

func TestServeTrimsTheName(t *testing.T) {
	got, err := artifacts.Serve("  report.pdf\t", "application/pdf")
	if err != nil {
		t.Fatal(err)
	}
	if got.Disposition != `inline; filename="report.pdf"` {
		t.Fatalf("disposition = %q", got.Disposition)
	}
}

func TestServeRefuses(t *testing.T) {
	cases := []struct {
		label, name, media string
	}{
		{"blank name", "   ", "application/pdf"},
		{"newline name", "report\n.pdf", "application/pdf"},
		{"return name", "report\r.pdf", "application/pdf"},
		{"quote name", `report".pdf`, "application/pdf"},
		{"backslash name", `report\.pdf`, "application/pdf"},
		{"newline media", "report.pdf", "application/pdf\nX-Evil: 1"},
		{"return media", "report.pdf", "application/pdf\r"},
	}
	for _, c := range cases {
		got, err := artifacts.Serve(c.name, c.media)
		if !errors.Is(err, artifacts.ErrArtifact) {
			t.Fatalf("%s: error = %v", c.label, err)
		}
		if got != (artifacts.Headers{}) {
			t.Fatalf("%s: headers = %#v", c.label, got)
		}
	}
}
