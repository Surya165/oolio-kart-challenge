package promo

import "sync/atomic"

// CodeSet holds the current set of valid codes and is safe for concurrent
// use. It knows nothing about promo rules or where codes come from.
//
// Reads are lock-free: a published map is never modified, and Replace swaps
// in a whole new map with a single atomic pointer store. A reader sees either
// the old set or the new one, never a mix, and never waits for a writer.
//
// The zero value is an empty set, ready to use.
type CodeSet struct {
	codes atomic.Pointer[map[string]struct{}]
}

var _ CodeLookup = (*CodeSet)(nil)

// NewCodeSet returns a set holding exactly the given codes.
func NewCodeSet(codes []string) *CodeSet {
	s := &CodeSet{}
	s.Replace(codes)
	return s
}

// Contains reports whether code is in the set.
func (s *CodeSet) Contains(code string) bool {
	m := s.codes.Load()
	if m == nil {
		return false
	}
	_, ok := (*m)[code]
	return ok
}

// Replace atomically swaps in a new set of codes. The new map is fully built
// before it is published.
func (s *CodeSet) Replace(codes []string) {
	m := make(map[string]struct{}, len(codes))
	for _, c := range codes {
		m[c] = struct{}{}
	}
	s.codes.Store(&m)
}

// Len returns the number of codes currently held.
func (s *CodeSet) Len() int {
	m := s.codes.Load()
	if m == nil {
		return 0
	}
	return len(*m)
}
