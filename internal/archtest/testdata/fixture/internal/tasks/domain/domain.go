// Package domain plants two violations: an I/O import and a direct import of
// its own adapters.
package domain

import (
	"database/sql"

	"example.com/fixture/internal/tasks/adapters/postgres"
)

var (
	_ = sql.ErrNoRows
	_ = postgres.Name
)

const Name = "domain"
