package domain

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path"
	"strings"
	"unicode/utf8"
)

const (
	// MaxAttachments is the most files one user line may carry.
	MaxAttachments = 10
	// MaxAttachmentBytes is the 20 MiB total cap across all files on one line.
	MaxAttachmentBytes = 20 << 20
	// MaxExtractedRunes caps the extracted text of a single attachment.
	MaxExtractedRunes = 64000
	// MaxDocxArchiveEntries caps the number of ZIP entries in a .docx upload.
	MaxDocxArchiveEntries = 2000
	// MaxDocxUncompressedBytes caps the total decompressed size of a .docx ZIP archive (32 MiB).
	MaxDocxUncompressedBytes = 32 << 20
	// MaxDocxCompressionRatio caps the decompressed-to-compressed byte ratio of a .docx archive.
	MaxDocxCompressionRatio = 100
)

var (
	// ErrAttachment is an attachment that fails name, type, magic-byte, or structural validation.
	ErrAttachment = errors.New("chat: attachment")
	// ErrAttachmentSize is attachments whose combined raw size exceeds MaxAttachmentBytes.
	ErrAttachmentSize = errors.New("chat: attachment size")
)

// RawAttachment is one uploaded file before validation and text extraction.
type RawAttachment struct {
	Name         string
	DeclaredMime string
	Bytes        []byte
}

// Attachment is the metadata of one validated chat attachment.
type Attachment struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Mime      string `json:"mime"`
	SizeBytes int    `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	// Extracted holds the trustfence-wrapped markdown in memory and in chat.findings
	// (kind='attachment'); json:"-" guarantees it is never stored in chat.lines.attachments JSONB.
	Extracted string `json:"-"`
}

var attachmentExtMime = map[string]string{
	".txt":  "text/plain",
	".md":   "text/markdown",
	".pdf":  "application/pdf",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".html": "text/html",
	".htm":  "text/html",
}

// CheckAttachments validates count, total size, file names, extensions, declared MIME types,
// magic bytes, and container bounds before any converter or store runs.
func CheckAttachments(files []RawAttachment) ([]Attachment, error) {
	if len(files) == 0 {
		return nil, nil
	}
	if len(files) > MaxAttachments {
		return nil, ErrAttachment
	}
	out := make([]Attachment, 0, len(files))
	totalBytes := 0
	for _, f := range files {
		rawName := strings.TrimSpace(f.Name)
		if rawName == "" || strings.ContainsAny(rawName, "/\\\x00") || strings.Contains(rawName, "..") {
			return nil, ErrAttachment
		}
		name := path.Base(rawName)
		if name == "" || name == "." || name == ".." || utf8.RuneCountInString(name) > 200 {
			return nil, ErrAttachment
		}
		if len(f.Bytes) == 0 {
			return nil, ErrAttachment
		}
		totalBytes += len(f.Bytes)
		if totalBytes > MaxAttachmentBytes {
			return nil, ErrAttachmentSize
		}
		ext := strings.ToLower(path.Ext(name))
		wantMime, ok := attachmentExtMime[ext]
		if !ok {
			return nil, ErrAttachment
		}
		if declared := baseMediaType(f.DeclaredMime); declared != "" && declared != "application/octet-stream" {
			allowed := declared == wantMime || (ext == ".md" && (declared == "text/plain" || declared == "text/x-markdown"))
			if !allowed {
				return nil, ErrAttachment
			}
		}
		if err := checkAttachmentMagic(ext, f.Bytes); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(f.Bytes)
		out = append(out, Attachment{
			Name:      name,
			Mime:      wantMime,
			SizeBytes: len(f.Bytes),
			SHA256:    hex.EncodeToString(sum[:]),
		})
	}
	return out, nil
}

func baseMediaType(raw string) string {
	if i := strings.IndexByte(raw, ';'); i >= 0 {
		raw = raw[:i]
	}
	return strings.TrimSpace(strings.ToLower(raw))
}

func checkAttachmentMagic(ext string, data []byte) error {
	switch ext {
	case ".pdf":
		return checkPDFMagic(data)
	case ".docx":
		return checkDocxArchive(data)
	case ".txt", ".md":
		return checkPlainText(data)
	case ".html", ".htm":
		return checkHTMLText(data)
	default:
		return ErrAttachment
	}
}

func hasExecutablePrefix(data []byte) bool {
	prefixes := [][]byte{
		{'M', 'Z'},
		{0x7f, 'E', 'L', 'F'},
		{0xfe, 0xed, 0xfa, 0xce},
		{0xfe, 0xed, 0xfa, 0xcf},
		{0xce, 0xfa, 0xed, 0xfe},
		{0xcf, 0xfa, 0xed, 0xfe},
		{0xca, 0xfe, 0xba, 0xbe},
	}
	for _, p := range prefixes {
		if bytes.HasPrefix(data, p) {
			return true
		}
	}
	return false
}

func checkPDFMagic(data []byte) error {
	if hasExecutablePrefix(data) || bytes.HasPrefix(data, []byte("#!")) {
		return ErrAttachment
	}
	head := data[:min(len(data), 1024)]
	if !bytes.Contains(head, []byte("%PDF-")) {
		return ErrAttachment
	}
	return nil
}

func checkDocxArchive(data []byte) error {
	if len(data) < 4 || !bytes.HasPrefix(data, []byte{'P', 'K', 0x03, 0x04}) {
		return ErrAttachment
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(zr.File) > MaxDocxArchiveEntries {
		return ErrAttachment
	}
	maxAllowed := int64(MaxDocxUncompressedBytes)
	if ratioCap := int64(len(data)) * MaxDocxCompressionRatio; ratioCap < maxAllowed {
		maxAllowed = ratioCap
	}
	var hasContentTypes, hasDocument bool
	var declaredTotal uint64
	var actualTotal int64
	maxAllowedU64 := uint64(maxAllowed)
	for _, entry := range zr.File {
		switch entry.Name {
		case "[Content_Types].xml":
			hasContentTypes = true
		case "word/document.xml":
			hasDocument = true
		}
		if entry.UncompressedSize64 > maxAllowedU64-declaredTotal {
			return ErrAttachment
		}
		declaredTotal += entry.UncompressedSize64
		n, err := readZipEntryBounded(entry, maxAllowed-actualTotal)
		if err != nil {
			return ErrAttachment
		}
		actualTotal += n
		if actualTotal > maxAllowed {
			return ErrAttachment
		}
	}
	if !hasContentTypes || !hasDocument {
		return ErrAttachment
	}
	return nil
}

func readZipEntryBounded(entry *zip.File, remaining int64) (int64, error) {
	rc, err := entry.Open()
	if err != nil {
		return 0, err
	}
	defer func() { _ = rc.Close() }()
	return io.Copy(io.Discard, io.LimitReader(rc, remaining+1))
}

func checkPlainText(data []byte) error {
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return ErrAttachment
	}
	if bytes.HasPrefix(data, []byte("%PDF-")) ||
		bytes.HasPrefix(data, []byte{'P', 'K', 0x03, 0x04}) ||
		hasExecutablePrefix(data) {
		return ErrAttachment
	}
	return nil
}

func checkHTMLText(data []byte) error {
	if err := checkPlainText(data); err != nil {
		return err
	}
	if bytes.HasPrefix(data, []byte("#!")) {
		return ErrAttachment
	}
	for _, b := range data[:min(len(data), 512)] {
		if b < 0x20 && b != '\t' && b != '\n' && b != '\v' && b != '\f' && b != '\r' && b != 0x1b {
			return ErrAttachment
		}
	}
	return nil
}
