package app

import (
	"context"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// CatalogReader reads catalogue rows and model kinds for vendors and accounts.
type CatalogReader interface {
	VendorCatalog(ctx context.Context, vendor string) ([]domain.CatalogRow, error)
	CatalogModel(ctx context.Context, vendor, model string) (domain.CatalogRow, bool, error)
	ModelKinds(ctx context.Context, pairs []domain.VendorModel) (map[domain.VendorModel]domain.ModelKind, error)
}
