// Package catalog holds the product catalogue.
package catalog

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
)

// Product matches the Product schema in the OpenAPI spec.
type Product struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Price    float64 `json:"price"`
	Category string  `json:"category"`
}

// ErrNotFound is returned when a product ID does not exist.
var ErrNotFound = errors.New("product not found")

// Store is the read side of the catalogue. The in-memory implementation is
// enough for a static menu; a database-backed one can satisfy the same
// interface when products become editable.
type Store interface {
	List(ctx context.Context) ([]Product, error)
	Get(ctx context.Context, id string) (Product, error)
}

// MemoryStore is an immutable, in-memory catalogue. It is safe for concurrent
// use because nothing mutates it after construction.
type MemoryStore struct {
	ordered []Product
	byID    map[string]Product
}

// NewMemoryStore builds a store, rejecting duplicate or empty IDs.
func NewMemoryStore(products []Product) (*MemoryStore, error) {
	s := &MemoryStore{byID: make(map[string]Product, len(products))}
	for _, p := range products {
		if p.ID == "" {
			return nil, fmt.Errorf("product %q has empty id", p.Name)
		}
		if _, dup := s.byID[p.ID]; dup {
			return nil, fmt.Errorf("duplicate product id %q", p.ID)
		}
		s.byID[p.ID] = p
		s.ordered = append(s.ordered, p)
	}
	return s, nil
}

//go:embed products.json
var seedJSON []byte

// NewSeeded returns the default menu embedded in the binary.
func NewSeeded() (*MemoryStore, error) {
	var products []Product
	if err := json.Unmarshal(seedJSON, &products); err != nil {
		return nil, fmt.Errorf("parse embedded products: %w", err)
	}
	return NewMemoryStore(products)
}

// List returns a copy so callers cannot mutate the catalogue.
func (s *MemoryStore) List(context.Context) ([]Product, error) {
	out := make([]Product, len(s.ordered))
	copy(out, s.ordered)
	return out, nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (Product, error) {
	p, ok := s.byID[id]
	if !ok {
		return Product{}, ErrNotFound
	}
	return p, nil
}
