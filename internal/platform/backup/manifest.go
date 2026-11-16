// Package backup archives and restores Upmo Postgres state and volume files.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

// FormatVersion is the archive layout version produced and read by this package.
const FormatVersion = 1

const (
	manifestEntryName        = "manifest.json"
	payloadPlainEntryName    = "payload.tar.gz"
	payloadEncEntryName      = "payload.tar.gz.enc"
	payloadManifestEntryName = "payload_manifest.json"
	dbMetaEntryName          = "db/meta.json"
	dbTablesPrefix           = "db/tables/"
	artifactsVolumePrefix    = "volumes/artifacts/"
	knowledgeVolumePrefix    = "volumes/knowledge/"

	maxMetadataBytes = 64 << 20 // 64 MiB cap for JSON metadata reads
)

// Manifest describes the backup archive and authenticates its payload entry.
type Manifest struct {
	FormatVersion  int              `json:"format_version"`
	BuildVersion   string           `json:"build_version"`
	Commit         string           `json:"commit"`
	BuildTime      string           `json:"build_time"`
	CreatedAt      time.Time        `json:"created_at"`
	MasterKeyID    string           `json:"master_key_id"`
	Encrypted      bool             `json:"encrypted"`
	PayloadSHA256  string           `json:"payload_sha256,omitempty"`
	SealedKey      *SealedKeyInfo   `json:"sealed_key,omitempty"`
	SchemaVersions map[string]int64 `json:"schema_versions"`
	Entries        []EntryChecksum  `json:"entries"`
}

type canonicalSchemaVersion struct {
	Schema  string `json:"schema"`
	Version int64  `json:"version"`
}

type canonicalManifestAAD struct {
	Domain         string                   `json:"domain"`
	FormatVersion  int                      `json:"format_version"`
	Encrypted      bool                     `json:"encrypted"`
	MasterKeyID    string                   `json:"master_key_id"`
	PayloadSHA256  string                   `json:"payload_sha256"`
	SchemaVersions []canonicalSchemaVersion `json:"schema_versions"`
}

// manifestAAD builds deterministic canonical additional authenticated data
// binding the outer manifest's security-critical fields to both the wrapped
// DEK and every encrypted payload stream chunk.
func manifestAAD(m Manifest) []byte {
	schemas := make([]canonicalSchemaVersion, 0, len(m.SchemaVersions))
	for s, v := range m.SchemaVersions {
		schemas = append(schemas, canonicalSchemaVersion{Schema: s, Version: v})
	}
	sort.Slice(schemas, func(i, j int) bool { return schemas[i].Schema < schemas[j].Schema })
	raw, err := json.Marshal(canonicalManifestAAD{
		Domain:         backupAADPrefix,
		FormatVersion:  m.FormatVersion,
		Encrypted:      m.Encrypted,
		MasterKeyID:    m.MasterKeyID,
		PayloadSHA256:  m.PayloadSHA256,
		SchemaVersions: schemas,
	})
	if err != nil {
		panic("backup: marshal manifest AAD: " + err.Error())
	}
	return raw
}

// SealedKeyInfo holds the wrapped data encryption key for encrypted archives.
type SealedKeyInfo struct {
	Mode       string `json:"mode"`
	KeyID      string `json:"key_id,omitempty"`
	Salt       string `json:"salt,omitempty"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

// EntryChecksum records the expected SHA-256 hex digest and byte length of one
// archive member.
type EntryChecksum struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// PayloadManifest lists SHA-256 digests and sizes for every entry inside the
// inner payload tarball.
type PayloadManifest struct {
	Entries []EntryChecksum `json:"entries"`
}

// Inspect reads and parses manifest.json from the archive at inPath without
// modifying any database or volume directory.
func Inspect(inPath string) (Manifest, error) {
	if strings.TrimSpace(inPath) == "" {
		return Manifest{}, errors.New("backup: input path is required")
	}
	f, err := os.Open(inPath) //nolint:gosec // CLI operator input path
	if err != nil {
		return Manifest{}, fmt.Errorf("backup: open archive: %w", err)
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return Manifest{}, fmt.Errorf("backup: read gzip stream: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Manifest{}, fmt.Errorf("backup: read tar entry: %w", err)
		}
		clean, err := cleanArchivePath(hdr.Name)
		if err != nil {
			return Manifest{}, err
		}
		if clean == manifestEntryName {
			return readManifest(tr, hdr.Size)
		}
	}
	return Manifest{}, fmt.Errorf("backup: %s missing from archive", manifestEntryName)
}

func readManifest(r io.Reader, size int64) (Manifest, error) {
	if size < 0 || size > maxMetadataBytes {
		return Manifest{}, fmt.Errorf("backup: %s size %d out of range", manifestEntryName, size)
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxMetadataBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("backup: read %s: %w", manifestEntryName, err)
	}
	if int64(len(raw)) > maxMetadataBytes {
		return Manifest{}, fmt.Errorf("backup: %s exceeds %d bytes", manifestEntryName, maxMetadataBytes)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("backup: parse %s: %w", manifestEntryName, err)
	}
	return m, nil
}

func cleanArchivePath(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, '\x00') || strings.ContainsRune(name, '\\') {
		return "", fmt.Errorf("backup: invalid archive path %q", name)
	}
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("backup: absolute archive path %q", name)
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("backup: unsafe archive path %q", name)
	}
	return clean, nil
}
