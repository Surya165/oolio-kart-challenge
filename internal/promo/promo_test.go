package promo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSetValidator(t *testing.T) {
	v := NewSetValidator([]string{"HAPPYHRS", "FIFTYOFF", "NINECHARS", "TENCHARS10"})
	tests := []struct {
		name string
		code string
		want bool
	}{
		{"valid 8 chars", "HAPPYHRS", true},
		{"valid 9 chars", "NINECHARS", true},
		{"valid 10 chars", "TENCHARS10", true},
		{"not in index", "SUPER100", false},
		{"lowercase variant", "happyhrs", false},
		{"mixed case variant", "HappyHrs", false},
		{"surrounding spaces", " HAPPYHRS ", false},
		{"too short", "HAPPYHR", false},
		{"too long", "TENCHARS100", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := v.Valid(context.Background(), tt.code)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("Valid(%q) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

func TestSetValidatorReplace(t *testing.T) {
	v := NewSetValidator([]string{"OLDCODE1"})
	v.Replace([]string{"NEWCODE1"})
	if ok, _ := v.Valid(context.Background(), "OLDCODE1"); ok {
		t.Error("old code still valid after Replace")
	}
	if ok, _ := v.Valid(context.Background(), "NEWCODE1"); !ok {
		t.Error("new code not valid after Replace")
	}
}

func TestIndexRoundTrip(t *testing.T) {
	var sb strings.Builder
	codes := []string{"FIFTYOFF", "HAPPYHRS"}
	if err := WriteIndex(&sb, codes, "header line\nsecond"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadIndex(strings.NewReader(sb.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, codes) {
		t.Fatalf("round trip = %v, want %v", got, codes)
	}
	if _, err := ReadIndex(strings.NewReader("SHORT\n")); err == nil {
		t.Fatal("expected error for out-of-range code in index")
	}
}

func TestFileIndex(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	idx := FileIndex{Path: filepath.Join(dir, "valid_coupons.txt")}

	if _, err := idx.Load(ctx); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load before publish: err = %v, want not-exist", err)
	}

	codes := []string{"FIFTYOFF", "HAPPYHRS"}
	if err := idx.Publish(ctx, codes, "built by test"); err != nil {
		t.Fatal(err)
	}
	got, err := idx.Load(ctx)
	if err != nil || !slices.Equal(got, codes) {
		t.Fatalf("Load = %v, %v; want %v", got, err, codes)
	}

	// Publishing again replaces the index, and leaves no temp files behind.
	if err := idx.Publish(ctx, []string{"BIRTHDAY"}, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := idx.Load(ctx); !slices.Equal(got, []string{"BIRTHDAY"}) {
		t.Fatalf("after republish Load = %v", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("dir has %d entries, want only the index (temp file leaked?)", len(entries))
	}
	info, _ := os.Stat(idx.Path)
	if info.Mode().Perm() != 0o644 {
		t.Errorf("index mode = %v, want 0644", info.Mode().Perm())
	}
}

// fakeSource is an in-memory IndexSource, standing in for S3 or any other
// backend: LoadFrom's rules must hold whatever the storage is.
type fakeSource struct {
	codes []string
	err   error
}

func (f fakeSource) Load(context.Context) ([]string, error) { return f.codes, f.err }

func TestLoadFrom(t *testing.T) {
	ctx := context.Background()
	v := NewSetValidator(nil)

	if n, err := v.LoadFrom(ctx, fakeSource{codes: []string{"HAPPYHRS", "FIFTYOFF"}}); err != nil || n != 2 {
		t.Fatalf("LoadFrom = %d, %v; want 2, nil", n, err)
	}

	// A failed or empty reload must keep the codes already being served.
	bad := []struct {
		name    string
		src     fakeSource
		wantErr error
	}{
		{"source error", fakeSource{err: os.ErrNotExist}, os.ErrNotExist},
		{"empty index", fakeSource{codes: nil}, ErrEmptyIndex},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := v.LoadFrom(ctx, tt.src); !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if ok, _ := v.Valid(ctx, "HAPPYHRS"); !ok || v.Len() != 2 {
				t.Fatalf("old index lost after bad reload (len %d)", v.Len())
			}
		})
	}

	// A file that only has a header is empty too.
	p := filepath.Join(t.TempDir(), "header-only.txt")
	os.WriteFile(p, []byte("# 0 valid codes\n"), 0o644)
	if _, err := v.LoadFrom(ctx, FileIndex{Path: p}); !errors.Is(err, ErrEmptyIndex) {
		t.Fatalf("header-only file: err = %v, want ErrEmptyIndex", err)
	}
}
