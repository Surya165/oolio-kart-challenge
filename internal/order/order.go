// Package order contains the order-placement use case. It knows nothing about
// HTTP: handlers translate requests into PlaceRequest and errors into status
// codes.
package order

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"

	"kart/internal/catalog"
	"kart/internal/promo"
)

// MaxItems bounds the number of line items in one order.
const MaxItems = 100

// Item is one order line. Quantity is a pointer so "missing" and "0" can be
// told apart during validation.
type Item struct {
	ProductID string `json:"productId"`
	Quantity  *int   `json:"quantity"`
}

// PlaceRequest matches the OrderReq schema.
type PlaceRequest struct {
	CouponCode string `json:"couponCode,omitempty"`
	Items      []Item `json:"items"`
}

// OrderItem is an order line in the response.
type OrderItem struct {
	ProductID string `json:"productId"`
	Quantity  int    `json:"quantity"`
}

// Order matches the Order schema.
type Order struct {
	ID       string            `json:"id"`
	Items    []OrderItem       `json:"items"`
	Products []catalog.Product `json:"products"`
}

// ValidationError means the request was well-formed JSON but broke a business
// rule. Handlers map it to 422.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

// Repository persists placed orders.
type Repository interface {
	Save(ctx context.Context, o Order) error
}

// Service places orders.
type Service struct {
	Catalog catalog.Store
	Promo   promo.Validator
	Repo    Repository
}

// Place validates the request, resolves products and stores the order.
func (s *Service) Place(ctx context.Context, req PlaceRequest) (Order, error) {
	if len(req.Items) == 0 {
		return Order{}, &ValidationError{"items", "at least one item is required"}
	}
	if len(req.Items) > MaxItems {
		return Order{}, &ValidationError{"items", fmt.Sprintf("at most %d items allowed", MaxItems)}
	}

	o := Order{Items: make([]OrderItem, 0, len(req.Items))}
	seen := make(map[string]bool)
	for i, it := range req.Items {
		field := fmt.Sprintf("items[%d]", i)
		if it.ProductID == "" {
			return Order{}, &ValidationError{field + ".productId", "is required"}
		}
		if it.Quantity == nil {
			return Order{}, &ValidationError{field + ".quantity", "is required"}
		}
		if *it.Quantity < 1 {
			return Order{}, &ValidationError{field + ".quantity", "must be at least 1"}
		}
		p, err := s.Catalog.Get(ctx, it.ProductID)
		if errors.Is(err, catalog.ErrNotFound) {
			return Order{}, &ValidationError{field + ".productId", fmt.Sprintf("product %q does not exist", it.ProductID)}
		}
		if err != nil {
			return Order{}, err
		}
		o.Items = append(o.Items, OrderItem{ProductID: it.ProductID, Quantity: *it.Quantity})
		if !seen[p.ID] {
			seen[p.ID] = true
			o.Products = append(o.Products, p)
		}
	}

	if req.CouponCode != "" {
		ok, err := s.Promo.Valid(ctx, req.CouponCode)
		if err != nil {
			return Order{}, fmt.Errorf("validate promo code: %w", err)
		}
		if !ok {
			return Order{}, &ValidationError{"couponCode", "invalid promo code"}
		}
	}

	o.ID = newID()
	if err := s.Repo.Save(ctx, o); err != nil {
		return Order{}, fmt.Errorf("save order: %w", err)
	}
	return o, nil
}

// newID returns a random UUIDv4 string.
func newID() string {
	var b [16]byte
	rand.Read(b[:]) // never returns an error (Go 1.24+ crashes instead)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// MemoryRepository keeps orders in process memory. Fine for a demo; a real
// deployment would use a database so orders survive restarts and are shared
// across replicas.
type MemoryRepository struct {
	mu     sync.Mutex
	orders map[string]Order
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{orders: make(map[string]Order)}
}

func (r *MemoryRepository) Save(_ context.Context, o Order) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.orders[o.ID] = o
	return nil
}

func (r *MemoryRepository) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.orders)
}
