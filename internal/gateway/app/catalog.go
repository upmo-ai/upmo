package app

import (
	"context"
	"fmt"
	"io"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// CatalogStore persists a refreshed model catalog.
type CatalogStore interface {
	ReplaceCatalog(ctx context.Context, version string, rows []domain.CatalogRow) error
	Capabilities(ctx context.Context, model string) (have []string, ok bool, err error)
}

// CatalogReader reads catalogue rows and model kinds for vendors and accounts.
type CatalogReader interface {
	VendorCatalog(ctx context.Context, vendor string) ([]domain.CatalogRow, error)
	CatalogModel(ctx context.Context, vendor, model string) (domain.CatalogRow, bool, error)
	ModelKinds(ctx context.Context, pairs []domain.VendorModel) (map[domain.VendorModel]domain.ModelKind, error)
}

// RefreshCatalog parses a models.dev document and stores it.
func RefreshCatalog(ctx context.Context, store CatalogStore, r io.Reader, version string) error {
	if store == nil {
		return fmt.Errorf("gateway: catalog store")
	}
	rows, err := domain.ParseCatalog(r, version)
	if err != nil {
		return err
	}
	return store.ReplaceCatalog(ctx, version, rows)
}
