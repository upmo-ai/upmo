// Package kernel holds types shared by more than one context. A type enters
// here only when it depends on nothing beyond the standard library and the
// UUID package.
package kernel

import (
	"database/sql/driver"
	"fmt"

	"github.com/google/uuid"
)

// ID is a UUIDv7 identifier. The type parameter stops one context's id being
// passed where another is required.
type ID[T any] struct {
	uuid.UUID
}

// NewID returns a new UUIDv7.
func NewID[T any]() (ID[T], error) {
	id, err := uuid.NewV7()
	if err != nil {
		return ID[T]{}, fmt.Errorf("kernel: new id: %w", err)
	}
	return ID[T]{UUID: id}, nil
}

// ParseID parses a UUIDv7 text form. Any UUID text is accepted so a value
// stored by Postgres can round-trip; NewID is what mints new ones.
func ParseID[T any](s string) (ID[T], error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return ID[T]{}, fmt.Errorf("kernel: parse id: %w", err)
	}
	return ID[T]{UUID: id}, nil
}

func (id ID[T]) String() string {
	return id.UUID.String()
}

func (id ID[T]) MarshalText() ([]byte, error) {
	return id.UUID.MarshalText()
}

func (id *ID[T]) UnmarshalText(text []byte) error {
	return id.UUID.UnmarshalText(text)
}

func (id ID[T]) Value() (driver.Value, error) {
	return id.UUID.Value()
}

func (id *ID[T]) Scan(src any) error {
	return id.UUID.Scan(src)
}
