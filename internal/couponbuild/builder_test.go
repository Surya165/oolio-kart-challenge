package couponbuild

import (
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeSource writes codes one per line, gzip-compressed when the name ends
// in .gz.
func writeSource(t *testing.T, dir, name string, codes ...string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	body := strings.Join(codes, "\n") + "\n"
	if !strings.HasSuffix(name, ".gz") {
		if _, err := f.WriteString(body); err != nil {
			t.Fatal(err)
		}
		return path
	}
	zw := gzip.NewWriter(f)
	if _, err := zw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// forEachEngine runs fn once per engine, built through the factory. Every
// engine must pass every test: this is what keeps them interchangeable.
func forEachEngine(t *testing.T, cfg Config, fn func(t *testing.T, b Builder)) {
	for _, e := range Engines() {
		t.Run(string(e), func(t *testing.T) {
			c := cfg
			c.TempDir = t.TempDir()
			b, err := New(e, c)
			if err != nil {
				t.Fatal(err)
			}
			fn(t, b)
		})
	}
}

func TestBuildRules(t *testing.T) {
	dir := t.TempDir()
	srcs := []string{
		writeSource(t, dir, "a.gz",
			"HAPPYHRS", "FIFTYOFF", "SUPER100", "ONLYINA1", "SEVEN77",
			"TENCHARS10", "ELEVENCHARS", "lowercase", "DUPINA99", "DUPINA99", "CRLFCODE\r"),
		writeSource(t, dir, "b.gz",
			"FIFTYOFF", "NINECHARS", "SEVEN77", "ELEVENCHARS", "HAPPYHRs", "DUPINA99", "CRLFCODE"),
		writeSource(t, dir, "c.txt",
			"HAPPYHRS", "FIFTYOFF", "NINECHARS", "TENCHARS10", "lowercase", "", "   "),
	}
	want := []string{
		"CRLFCODE",   // trailing \r stripped, in a and b
		"DUPINA99",   // twice in a, once in b: counts as 2 files
		"FIFTYOFF",   // all three files
		"HAPPYHRS",   // a and c (b has a different-case variant)
		"NINECHARS",  // 9 chars, b and c
		"TENCHARS10", // 10 chars (upper bound), a and c
		"lowercase",  // exact match: case preserved, not normalised
	}
	// Not wanted: SUPER100/ONLYINA1 (one file), SEVEN77 (7 chars),
	// ELEVENCHARS (11 chars), HAPPYHRs (one file), blank lines.
	// Partitioning must never change the answer, only the resource profile.
	for _, parts := range []int{1, 3, 16} {
		forEachEngine(t, Config{MinSources: 2, Partitions: parts}, func(t *testing.T, b Builder) {
			got, err := b.Build(context.Background(), srcs)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("partitions=%d: Build() =\n %v\nwant\n %v", parts, got, want)
			}
		})
	}
}

func TestBuildMinSources(t *testing.T) {
	dir := t.TempDir()
	srcs := []string{
		writeSource(t, dir, "a", "INALLTHREE", "INTWOONLY"),
		writeSource(t, dir, "b", "INALLTHREE", "INTWOONLY"),
		writeSource(t, dir, "c", "INALLTHREE"),
	}
	tests := []struct {
		min  int
		want []string
	}{
		{1, []string{"INALLTHREE", "INTWOONLY"}},
		{2, []string{"INALLTHREE", "INTWOONLY"}},
		{3, []string{"INALLTHREE"}},
		{4, nil},
	}
	for _, tt := range tests {
		forEachEngine(t, Config{MinSources: tt.min}, func(t *testing.T, b Builder) {
			got, err := b.Build(context.Background(), srcs)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("min=%d: got %v, want %v", tt.min, got, tt.want)
			}
		})
	}
}

func TestBuildDuplicatesWithinOneFileDoNotCount(t *testing.T) {
	dir := t.TempDir()
	srcs := []string{
		writeSource(t, dir, "a", "REPEATED", "REPEATED", "REPEATED"),
		writeSource(t, dir, "b", "SOMETHING"),
	}
	forEachEngine(t, Config{}, func(t *testing.T, b Builder) {
		got, err := b.Build(context.Background(), srcs)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("expected no valid codes, got %v", got)
		}
	})
}

func TestBuildPathWithQuote(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "it's here")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcs := []string{writeSource(t, dir, "a", "QUOTEDOK"), writeSource(t, dir, "b", "QUOTEDOK")}
	forEachEngine(t, Config{}, func(t *testing.T, b Builder) {
		got, err := b.Build(context.Background(), srcs)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, []string{"QUOTEDOK"}) {
			t.Fatalf("got %v", got)
		}
	})
}

func TestBuildErrors(t *testing.T) {
	forEachEngine(t, Config{}, func(t *testing.T, b Builder) {
		if _, err := b.Build(context.Background(), []string{"/does/not/exist"}); err == nil {
			t.Error("expected error for missing source")
		}
		if _, err := b.Build(context.Background(), nil); err == nil {
			t.Error("expected error for no sources")
		}
	})
}

func TestNew(t *testing.T) {
	if _, err := New("postgres", Config{}); err == nil {
		t.Error("expected error for unknown engine")
	}
	if _, err := New(EngineHashJoin, Config{MinSources: -1}); err == nil {
		t.Error("expected error for negative MinSources")
	}
	for _, e := range Engines() {
		if _, err := New(e, Config{}); err != nil {
			t.Errorf("New(%q): %v", e, err)
		}
	}
}
