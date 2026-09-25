// Package promo decides whether a promo code is valid.
//
// Validation is split in two phases:
//   - Build (offline, see internal/couponbuild and cmd/couponindex): stream the
//     raw coupon files once and derive the small set of codes that satisfy the
//     rules, published as an index (index.go).
//   - Serve (this package): load that index into memory and answer lookups.
//
// The serving side has three parts, each with one job:
//   - RuleValidator (this file): applies the promo rules. Owns no state.
//   - CodeSet (codeset.go): holds the current codes and swaps them atomically.
//     Knows no rules.
//   - Reload (reload.go): loads an index from a source and decides whether to
//     accept it. Knows neither rules nor storage internals.
//
// Request handlers only ever see the Validator interface, so any of these can
// be replaced (e.g. a Redis- or Postgres-backed lookup) without touching HTTP
// code.
package promo

import "context"

// Length bounds from the challenge rules.
const (
	MinLen = 8
	MaxLen = 10
)

// Validator reports whether a promo code can be applied to an order.
type Validator interface {
	Valid(ctx context.Context, code string) (bool, error)
}

// CodeLookup answers whether a code is in the current set of valid codes.
// CodeSet is the in-memory implementation; a database-backed one could
// satisfy it too.
type CodeLookup interface {
	Contains(code string) bool
}

// ValidLength reports whether code satisfies the length rule.
func ValidLength(code string) bool {
	return len(code) >= MinLen && len(code) <= MaxLen
}

// RuleValidator applies the promo rules: the length rule first (cheap, no
// lookup needed), then membership in the precomputed set. New rules (expiry,
// per-merchant codes, ...) belong here; storage does not.
//
// Codes are matched exactly (case-sensitive): every code in the source files
// is uppercase alphanumeric, and we treat codes as identifiers rather than
// guess at normalisation.
type RuleValidator struct {
	Codes CodeLookup
}

var _ Validator = RuleValidator{}

func (v RuleValidator) Valid(_ context.Context, code string) (bool, error) {
	if !ValidLength(code) {
		return false, nil
	}
	return v.Codes.Contains(code), nil
}
