// Package snapshot embeds the pinned models.dev catalog snapshot loaded by
// migrations on fresh or older installations.
package snapshot

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

//go:embed models_dev.json.gz
var embeddedGZ []byte

//go:embed models_dev.version
var embeddedVersion string

const maxSnapshotBytes = 50 << 20

// Load decodes, parses, and validates the embedded models.dev snapshot.
func Load() (version string, asOf time.Time, rows []domain.CatalogRow, err error) {
	return Decode(embeddedGZ, embeddedVersion)
}

// Decode decompresses, parses, and validates a gzipped models.dev snapshot and
// its RFC3339 build timestamp.
func Decode(gz []byte, versionRaw string) (version string, asOf time.Time, rows []domain.CatalogRow, err error) {
	version = strings.TrimSpace(versionRaw)
	if version == "" {
		return "", time.Time{}, nil, fmt.Errorf("gateway: snapshot version is empty")
	}
	asOf, err = time.Parse(time.RFC3339, version)
	if err != nil {
		return "", time.Time{}, nil, fmt.Errorf("gateway: snapshot version %q: %w", version, err)
	}
	asOf = asOf.UTC()
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return "", time.Time{}, nil, fmt.Errorf("gateway: snapshot gzip: %w", err)
	}
	defer func() { _ = zr.Close() }()
	raw, err := io.ReadAll(io.LimitReader(zr, maxSnapshotBytes+1))
	if err != nil {
		return "", time.Time{}, nil, fmt.Errorf("gateway: snapshot read: %w", err)
	}
	if int64(len(raw)) > maxSnapshotBytes {
		return "", time.Time{}, nil, fmt.Errorf("gateway: snapshot exceeds size cap")
	}
	rows, err = domain.ParseCatalog(bytes.NewReader(raw), version)
	if err != nil {
		return "", time.Time{}, nil, err
	}
	if err := domain.ValidateCatalog(rows, 0); err != nil {
		return "", time.Time{}, nil, err
	}
	return version, asOf, rows, nil
}
