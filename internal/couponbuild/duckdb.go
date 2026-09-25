package couponbuild

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"

	_ "github.com/duckdb/duckdb-go/v2" // registers the "duckdb" database/sql driver

	"kart/internal/promo"
)

// DuckDB expresses the build as a SQL query over the raw files, executed by
// an embedded DuckDB (no server). DuckDB reads gzip directly, runs the GROUP
// BY as a parallel hash aggregate, and spills to TempDir when it exceeds
// MemoryLimit: the same hash join HashJoin does by hand, delegated to a query
// engine.
//
// Spilling is less compact than HashJoin's buckets (each spilled row carries
// its hash and aggregate state): on the challenge data a single pass spilled
// ~6 GB versus ~3 GB for HashJoin. Partitions > 1 splits the work into
// independent passes to cap memory and disk, at the cost of re-reading the
// input once per pass.
type DuckDB struct {
	cfg Config
}

func (d *DuckDB) Build(ctx context.Context, sources []string) ([]string, error) {
	if err := checkSources(sources); err != nil {
		return nil, err
	}
	// read_csv fails with a less useful message on a missing file; check first.
	for _, s := range sources {
		if _, err := os.Stat(s); err != nil {
			return nil, err
		}
	}
	tmp, err := os.MkdirTemp(d.cfg.TempDir, "couponbuild-duckdb-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	db, err := sql.Open("duckdb", "") // in-memory database
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // SET statements are per connection

	for _, stmt := range []string{
		"SET memory_limit = " + sqlString(d.cfg.MemoryLimit),
		"SET temp_directory = " + sqlString(tmp),
		"SET preserve_insertion_order = false", // we sort at the end anyway
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return nil, fmt.Errorf("duckdb %q: %w", stmt, err)
		}
	}

	// Each pass handles the codes with hash(code) % passes == k, so the
	// aggregate (and its spill) is ~1/passes the size. All copies of a code
	// hash the same, so passes are independent: the same idea as HashJoin's
	// buckets, traded against re-reading the files once per pass.
	passes := d.cfg.Partitions
	query := buildQuery(sources)
	var codes []string
	for k := 0; k < passes; k++ {
		got, err := d.pass(ctx, db, query, passes, k)
		if err != nil {
			return nil, err
		}
		codes = append(codes, got...)
	}
	sort.Strings(codes)
	return codes, nil
}

func (d *DuckDB) pass(ctx context.Context, db *sql.DB, query string, passes, k int) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, d.cfg.MinSources, passes, k)
	if err != nil {
		return nil, fmt.Errorf("duckdb query (pass %d/%d): %w", k+1, passes, err)
	}
	defer rows.Close()
	var codes []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		codes = append(codes, c)
	}
	return codes, rows.Err()
}

// buildQuery returns, for three sources:
//
//	SELECT code FROM (
//	  SELECT trim(...) AS code, 1::UBIGINT AS bit FROM read_csv('f1', ...)
//	  UNION ALL SELECT trim(...), 2 FROM read_csv('f2', ...)
//	  UNION ALL SELECT trim(...), 4 FROM read_csv('f3', ...)
//	) WHERE strlen(code) BETWEEN 8 AND 10 AND hash(code) % $2 = $3
//	GROUP BY code HAVING bit_count(bit_or(bit)) >= $1
//
// This mirrors HashJoin exactly: each file contributes one bit, OR-ing the
// bits per code records which files it was seen in (so repeats inside one
// file count once), and the popcount is the number of distinct files. It is
// a single hash aggregate; an earlier version with DISTINCT per file plus an
// outer GROUP BY ran two aggregates and spilled ~2x more to disk.
//
// File paths cannot be bind parameters in a table function, so they are
// embedded as escaped SQL string literals.
func buildQuery(sources []string) string {
	parts := make([]string, len(sources))
	for i, src := range sources {
		parts[i] = fmt.Sprintf(`SELECT trim(code, ' ' || chr(9) || chr(13)) AS code, %d::UBIGINT AS bit
      FROM read_csv(%s, header = false, auto_detect = false, columns = {'code': 'VARCHAR'},
                    delim = chr(9), quote = '', escape = '', strict_mode = false)`, uint64(1)<<i, sqlString(src))
	}
	return "SELECT code FROM (\n    " + strings.Join(parts, "\n    UNION ALL\n    ") +
		fmt.Sprintf("\n) WHERE strlen(code) BETWEEN %d AND %d AND hash(code) %% $2 = $3\nGROUP BY code HAVING bit_count(bit_or(bit)) >= $1", promo.MinLen, promo.MaxLen)
}

// sqlString quotes s as a SQL string literal.
func sqlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
