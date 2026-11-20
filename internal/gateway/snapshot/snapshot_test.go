package snapshot

import (
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

func TestEmbeddedSnapshot_DecodesAndValidates(t *testing.T) {
	version, asOf, rows, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if version == "" || asOf.IsZero() {
		t.Fatalf("empty version=%q or asOf=%v", version, asOf)
	}
	if len(rows) < domain.MinCatalogModels {
		t.Fatalf("rows = %d, want at least %d", len(rows), domain.MinCatalogModels)
	}
	if err := domain.ValidateCatalog(rows, 0); err != nil {
		t.Fatalf("ValidateCatalog failed: %v", err)
	}
}

func TestDecodeRejectsCorruptSnapshot(t *testing.T) {
	if _, _, _, err := Decode([]byte("not-gzip"), "2026-11-19T00:00:00Z"); err == nil {
		t.Fatal("expected gzip error")
	}
	if _, _, _, err := Decode([]byte("x"), "not-a-date"); err == nil {
		t.Fatal("expected version error")
	}
}
