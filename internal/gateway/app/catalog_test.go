package app

import (
	"context"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

type memCatalog struct {
	version string
	rows    []domain.CatalogRow
}

func (m *memCatalog) ReplaceCatalog(_ context.Context, version string, rows []domain.CatalogRow) error {
	m.version = version
	m.rows = append([]domain.CatalogRow(nil), rows...)
	return nil
}

func (m *memCatalog) Capabilities(_ context.Context, model string) ([]string, bool, error) {
	for _, row := range m.rows {
		if row.Model == model {
			return row.Caps, true, nil
		}
	}
	return nil, false, nil
}

func TestRefreshCatalog(t *testing.T) {
	store := &memCatalog{}
	body := `{"p":{"models":{"m1":{"tool_call":true,"modalities":{"input":["text"]},"cost":{"input":1,"output":2}}}}}`
	if err := RefreshCatalog(context.Background(), store, strings.NewReader(body), "snap-1"); err != nil {
		t.Fatal(err)
	}
	if store.version != "snap-1" || len(store.rows) != 1 || store.rows[0].Model != "m1" {
		t.Fatalf("store = %+v", store)
	}
	have, ok, err := store.Capabilities(context.Background(), "m1")
	if err != nil || !ok || len(have) != 1 || have[0] != domain.CapTools {
		t.Fatalf("caps = %v %v %v", have, ok, err)
	}
}
