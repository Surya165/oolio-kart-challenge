// Package promo decides whether a promo code is valid.
//
// Validation is split in two phases:
//   - Build (offline, see internal/couponbuild and cmd/couponindex): stream the
//     raw coupon files once and derive the small set of codes that satisfy the
//     rules, written as an index file (index.go describes the format).
//   - Serve (this package): load that derived set into memory and answer lookups.
//
// Request handlers only ever see the Validator interface, so the backing
// implementation (in-memory set, Redis, Postgres, ...) can change without
// touching HTTP code.
package promo

import (
	"context"
	"sync/atomic"
)

// Length bounds from the challenge rules.
const (
	MinLen = 8
	MaxLen = 10
)

// Validator reports whether a promo code can be applied to an order.
type Validator interface {
	Valid(ctx context.Context, code string) (bool, error)
}

// ValidLength reports whether code satisfies the length rule. It is a cheap
// pre-check that lets us reject obviously bad codes before any lookup.
func ValidLength(code string) bool {
	return len(code) >= MinLen && len(code) <= MaxLen
}

// SetValidator is an in-memory Validator backed by a precomputed set of valid
// codes. Lookups are lock-free: the set is immutable once published, and
// Replace swaps it atomically, so a refresh never blocks readers.
type SetValidator struct {
	codes atomic.Pointer[map[string]struct{}]
}

// NewSetValidator returns a validator holding exactly the given codes.
func NewSetValidator(codes []string) *SetValidator {
	v := &SetValidator{}
	v.Replace(codes)
	return v
}

// Valid matches codes exactly (case-sensitive). Every code in the source files
// is uppercase alphanumeric; we treat codes as identifiers and do not guess at
// normalisation.
func (v *SetValidator) Valid(_ context.Context, code string) (bool, error) {
	if !ValidLength(code) {
		return false, nil
	}
	_, ok := (*v.codes.Load())[code]
	return ok, nil
}

// Replace atomically swaps in a new set of valid codes.
func (v *SetValidator) Replace(codes []string) {
	m := make(map[string]struct{}, len(codes))
	for _, c := range codes {
		m[c] = struct{}{}
	}
	v.codes.Store(&m)
}

// Len returns the number of codes currently loaded.
func (v *SetValidator) Len() int { return len(*v.codes.Load()) }
