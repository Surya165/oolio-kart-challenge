package promo

import (
	"context"
	"fmt"
)

// Replacer is anything that can atomically swap in a new set of codes.
type Replacer interface {
	Replace(codes []string)
}

// Reload loads an index from src and, if it is acceptable, swaps it into dst.
// This is the one place the acceptance policy lives, whatever the storage:
// if loading fails or the index is empty, dst is left untouched, so a bad
// reload never takes coupons offline. It returns the number of codes loaded.
func Reload(ctx context.Context, src IndexSource, dst Replacer) (int, error) {
	codes, err := src.Load(ctx)
	if err != nil {
		return 0, err
	}
	if len(codes) == 0 {
		return 0, fmt.Errorf("%v: %w", src, ErrEmptyIndex)
	}
	dst.Replace(codes)
	return len(codes), nil
}
