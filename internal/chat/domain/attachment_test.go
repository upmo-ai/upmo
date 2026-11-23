package domain

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func buildZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestCheckAttachmentsMagicBytesZipBoundsAndLineLimits(t *testing.T) {
	// Line limits: 32,000 accepted, 32,001 rejected.
	ok32k := strings.Repeat("a", LineRunes)
	if got, err := Line(ok32k); err != nil || len(got) != 32000 {
		t.Fatalf("Line(32000) = len %d err %v", len(got), err)
	}
	if _, err := Line(strings.Repeat("a", LineRunes+1)); !errors.Is(err, ErrLine) {
		t.Fatalf("Line(32001) = %v, want ErrLine", err)
	}
	if ReplyRunes != 64000 {
		t.Fatalf("ReplyRunes = %d, want 64000", ReplyRunes)
	}

	// File count: 10 accepted, 11 rejected.
	ten := make([]RawAttachment, MaxAttachments)
	for i := range ten {
		ten[i] = RawAttachment{Name: "a.txt", Bytes: []byte("hello")}
	}
	if got, err := CheckAttachments(ten); err != nil || len(got) != MaxAttachments {
		t.Fatalf("10 files = %d err %v", len(got), err)
	}
	eleven := append(append([]RawAttachment(nil), ten...), RawAttachment{Name: "b.txt", Bytes: []byte("hello")})
	if _, err := CheckAttachments(eleven); !errors.Is(err, ErrAttachment) {
		t.Fatalf("11 files = %v, want ErrAttachment", err)
	}

	// Total size: 20 MiB accepted, 20 MiB + 1 rejected.
	half := bytes.Repeat([]byte("a"), MaxAttachmentBytes/2)
	if _, err := CheckAttachments([]RawAttachment{
		{Name: "one.txt", Bytes: half},
		{Name: "two.txt", Bytes: half},
	}); err != nil {
		t.Fatalf("20 MiB exact = %v", err)
	}
	if _, err := CheckAttachments([]RawAttachment{
		{Name: "one.txt", Bytes: half},
		{Name: "two.txt", Bytes: half},
		{Name: "three.txt", Bytes: []byte("x")},
	}); !errors.Is(err, ErrAttachmentSize) {
		t.Fatalf("20 MiB + 1 = %v, want ErrAttachmentSize", err)
	}

	// Empty file, path traversal, and invalid names.
	for name, f := range map[string]RawAttachment{
		"empty":          {Name: "empty.txt", Bytes: nil},
		"path traversal": {Name: "../../etc/passwd", Bytes: []byte("root:x:0:0")},
		"traversal txt":  {Name: "../../secret.txt", Bytes: []byte("secret")},
		"long name":      {Name: strings.Repeat("a", 201) + ".txt", Bytes: []byte("hi")},
		"unsupported":    {Name: "run.sh", Bytes: []byte("#!/bin/sh\n")},
	} {
		if _, err := CheckAttachments([]RawAttachment{f}); !errors.Is(err, ErrAttachment) {
			t.Errorf("%s: err = %v, want ErrAttachment", name, err)
		}
	}

	// Text / Markdown / HTML validation.
	for name, f := range map[string]RawAttachment{
		"txt nul":     {Name: "a.txt", Bytes: []byte("hi\x00there")},
		"txt utf8":    {Name: "a.txt", Bytes: []byte{0xff, 0xfe}},
		"md mz":       {Name: "a.md", Bytes: []byte("MZ\x90\x00")},
		"md elf":      {Name: "a.md", Bytes: []byte("\x7fELF\x02")},
		"html nul":    {Name: "a.html", Bytes: []byte("<p>hi\x00</p>")},
		"html utf8":   {Name: "a.html", Bytes: []byte{0x80, 0x81}},
		"html ctrl":   {Name: "a.html", Bytes: []byte("<p>\x07bell</p>")},
		"html binary": {Name: "a.htm", Bytes: []byte("%PDF-1.7")},
	} {
		if _, err := CheckAttachments([]RawAttachment{f}); !errors.Is(err, ErrAttachment) {
			t.Errorf("%s: err = %v, want ErrAttachment", name, err)
		}
	}
	if got, err := CheckAttachments([]RawAttachment{
		{Name: "notes.md", DeclaredMime: "text/plain; charset=utf-8", Bytes: []byte("# Title\nBody")},
		{Name: "page.htm", DeclaredMime: "text/html", Bytes: []byte("<!doctype html><html><body>Hi</body></html>")},
	}); err != nil || len(got) != 2 || got[0].Mime != "text/markdown" || got[1].Mime != "text/html" {
		t.Fatalf("valid text/html = %+v %v", got, err)
	}

	// Declared MIME mismatch.
	if _, err := CheckAttachments([]RawAttachment{
		{Name: "doc.pdf", DeclaredMime: "image/png", Bytes: []byte("%PDF-1.7\n")},
	}); !errors.Is(err, ErrAttachment) {
		t.Fatalf("mime mismatch = %v, want ErrAttachment", err)
	}

	// PDF magic header.
	if _, err := CheckAttachments([]RawAttachment{
		{Name: "ok.pdf", DeclaredMime: "application/pdf", Bytes: []byte("%PDF-1.7\n1 0 obj\nendobj\n")},
	}); err != nil {
		t.Fatalf("valid pdf = %v", err)
	}
	for name, raw := range map[string][]byte{
		"mz":        []byte("MZ\x90\x00%PDF-1.7"),
		"elf":       []byte("\x7fELF%PDF-1.7"),
		"shebang":   []byte("#!/bin/sh\n%PDF-1.7"),
		"no header": []byte("just plain text"),
	} {
		if _, err := CheckAttachments([]RawAttachment{{Name: "bad.pdf", Bytes: raw}}); !errors.Is(err, ErrAttachment) {
			t.Errorf("bad pdf %s: err = %v, want ErrAttachment", name, err)
		}
	}

	// DOCX valid OOXML ZIP vs invalid containers and zip bomb (>100:1 ratio).
	validDocx := buildZip(t, map[string][]byte{
		"[Content_Types].xml": []byte(`<Types/>`),
		"word/document.xml":   []byte(`<w:document/>`),
	})
	if got, err := CheckAttachments([]RawAttachment{{Name: "ok.docx", Bytes: validDocx}}); err != nil || len(got) != 1 || got[0].SHA256 == "" {
		t.Fatalf("valid docx = %+v %v", got, err)
	}
	missingDoc := buildZip(t, map[string][]byte{
		"[Content_Types].xml": []byte(`<Types/>`),
	})
	if _, err := CheckAttachments([]RawAttachment{{Name: "missing.docx", Bytes: missingDoc}}); !errors.Is(err, ErrAttachment) {
		t.Fatalf("docx missing word/document.xml = %v, want ErrAttachment", err)
	}
	if _, err := CheckAttachments([]RawAttachment{{Name: "notzip.docx", Bytes: []byte("not a zip")}}); !errors.Is(err, ErrAttachment) {
		t.Fatalf("docx non-zip = %v, want ErrAttachment", err)
	}
	// Zip bomb: 1 MiB of zeros compresses to ~1 KiB (> 100:1 compression ratio).
	bomb := buildZip(t, map[string][]byte{
		"[Content_Types].xml": []byte(`<Types/>`),
		"word/document.xml":   bytes.Repeat([]byte("A"), 1<<20),
	})
	if _, err := CheckAttachments([]RawAttachment{{Name: "bomb.docx", Bytes: bomb}}); !errors.Is(err, ErrAttachment) {
		t.Fatalf("docx ratio bomb = %v, want ErrAttachment", err)
	}

	// Spoofed ZIP headers declaring a small uncompressed size (10 bytes) while the
	// entry actually decompresses to 1 MiB (exceeding the ratio cap).
	spoofedSmall := patchZipUncompressedSize(t, bomb, "word/document.xml", 10)
	zr, err := zip.NewReader(bytes.NewReader(spoofedSmall), int64(len(spoofedSmall)))
	if err != nil {
		t.Fatalf("spoofedSmall zip.NewReader = %v", err)
	}
	for _, entry := range zr.File {
		if entry.Name == "word/document.xml" && entry.UncompressedSize64 != 10 {
			t.Fatalf("spoofedSmall declared size = %d, want 10", entry.UncompressedSize64)
		}
	}
	if _, err := CheckAttachments([]RawAttachment{{Name: "spoofed-small.docx", Bytes: spoofedSmall}}); !errors.Is(err, ErrAttachment) {
		t.Fatalf("docx spoofed small declared size = %v, want ErrAttachment", err)
	}

	// Spoofed ZIP headers declaring an uncompressed size over 32 MiB on a tiny entry.
	spoofedLarge := patchZipUncompressedSize(t, validDocx, "word/document.xml", MaxDocxUncompressedBytes+1)
	if _, err := CheckAttachments([]RawAttachment{{Name: "spoofed-large.docx", Bytes: spoofedLarge}}); !errors.Is(err, ErrAttachment) {
		t.Fatalf("docx spoofed large declared size = %v, want ErrAttachment", err)
	}
}

// patchZipUncompressedSize rewrites the 32-bit uncompressed size field for target
// in both the Local File Header (PK\x03\x04) and Central Directory Header (PK\x01\x02).
func patchZipUncompressedSize(t *testing.T, raw []byte, target string, size uint32) []byte {
	t.Helper()
	out := append([]byte(nil), raw...)
	nameBytes := []byte(target)
	patchedLocal, patchedCentral := false, false
	for i := 0; i+30 < len(out); i++ {
		if out[i] == 'P' && out[i+1] == 'K' && out[i+2] == 0x03 && out[i+3] == 0x04 {
			nameLen := int(binary.LittleEndian.Uint16(out[i+26 : i+28]))
			if i+30+nameLen <= len(out) && bytes.Equal(out[i+30:i+30+nameLen], nameBytes) {
				// Clear data descriptor flag (bit 3) so archive/zip trusts header size.
				flags := binary.LittleEndian.Uint16(out[i+6 : i+8])
				binary.LittleEndian.PutUint16(out[i+6:i+8], flags&^0x08)
				binary.LittleEndian.PutUint32(out[i+22:i+26], size)
				patchedLocal = true
			}
		}
		if i+46 < len(out) && out[i] == 'P' && out[i+1] == 'K' && out[i+2] == 0x01 && out[i+3] == 0x02 {
			nameLen := int(binary.LittleEndian.Uint16(out[i+28 : i+30]))
			if i+46+nameLen <= len(out) && bytes.Equal(out[i+46:i+46+nameLen], nameBytes) {
				flags := binary.LittleEndian.Uint16(out[i+8 : i+10])
				binary.LittleEndian.PutUint16(out[i+8:i+10], flags&^0x08)
				binary.LittleEndian.PutUint32(out[i+24:i+28], size)
				patchedCentral = true
			}
		}
	}
	if !patchedLocal || !patchedCentral {
		t.Fatalf("failed to patch zip headers for %s: local=%v central=%v", target, patchedLocal, patchedCentral)
	}
	return out
}
