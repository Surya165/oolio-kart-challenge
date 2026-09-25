// Package couponbuild derives the set of valid promo codes from the raw
// coupon files. It is the offline half of promo validation: its output is the
// index file that the server loads (see internal/promo).
//
// Two interchangeable engines implement the same Builder interface:
//
//   - hashjoin: a hand-written partitioned hash join in pure Go. No
//     dependencies, memory bounded by the partition count.
//   - duckdb: the same join expressed as one SQL query, executed by an
//     embedded DuckDB. DuckDB spills to disk on its own.
//
// Both must produce identical output for identical input; the conformance
// test in builder_test.go runs every engine against the same fixtures.
//
// This package is only imported by cmd/couponindex. The API server never
// links it, so the server binary stays pure Go (DuckDB requires cgo).
package couponbuild

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Builder computes the sorted set of valid codes from a list of source files
// (gzip-compressed or plain, one code per line).
type Builder interface {
	Build(ctx context.Context, sources []string) ([]string, error)
}

// Engine names a Builder implementation.
type Engine string

const (
	EngineHashJoin Engine = "hashjoin"
	EngineDuckDB   Engine = "duckdb"
)

// Engines lists every engine New accepts, in a stable order.
func Engines() []Engine { return []Engine{EngineHashJoin, EngineDuckDB} }

// Config holds settings shared by all engines plus a few engine-specific
// knobs. Zero values mean "use the default".
type Config struct {
	// MinSources is how many distinct files a code must appear in. Default 2.
	MinSources int
	// TempDir is where engines may spill to disk. Default: OS temp dir.
	TempDir string

	// Partitions splits the join into independent pieces by hash(code).
	// hashjoin: number of on-disk buckets, one input pass; peak memory is
	// roughly total candidates / Partitions. Default 64.
	// duckdb: number of query passes, each re-reading the input; peak memory
	// and spill are roughly 1/Partitions of a single pass. Default 1.
	Partitions int
	// MemoryLimit (duckdb only): DuckDB memory cap, e.g. "1GB". Default "1GB".
	MemoryLimit string
}

// New is the factory: it returns the Builder for the named engine.
func New(engine Engine, cfg Config) (Builder, error) {
	if cfg.MinSources == 0 {
		cfg.MinSources = 2
	}
	if cfg.Partitions < 0 {
		return nil, fmt.Errorf("Partitions must be >= 0, got %d", cfg.Partitions)
	}
	if cfg.MinSources < 1 {
		return nil, fmt.Errorf("MinSources must be >= 1, got %d", cfg.MinSources)
	}
	switch engine {
	case EngineHashJoin:
		if cfg.Partitions == 0 {
			cfg.Partitions = 64
		}
		return &HashJoin{cfg: cfg}, nil
	case EngineDuckDB:
		if cfg.MemoryLimit == "" {
			cfg.MemoryLimit = "1GB"
		}
		if cfg.Partitions == 0 {
			cfg.Partitions = 1
		}
		return &DuckDB{cfg: cfg}, nil
	default:
		names := make([]string, 0, len(Engines()))
		for _, e := range Engines() {
			names = append(names, string(e))
		}
		return nil, fmt.Errorf("unknown engine %q (want one of: %s)", engine, strings.Join(names, ", "))
	}
}

func checkSources(sources []string) error {
	if len(sources) == 0 || len(sources) > 64 {
		return fmt.Errorf("need 1-64 sources, got %d", len(sources))
	}
	if slices.Contains(sources, "") {
		return fmt.Errorf("empty source path")
	}
	return nil
}
