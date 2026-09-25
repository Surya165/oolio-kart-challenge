package promo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestRuleValidator(t *testing.T) {
	v := RuleValidator{Codes: NewCodeSet([]string{"HAPPYHRS", "FIFTYOFF", "NINECHARS", "TENCHARS10"})}
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

// lookupFunc adapts a function to CodeLookup, to show RuleValidator works
// against any lookup, not just CodeSet.
type lookupFunc func(string) bool

func (f lookupFunc) Contains(code string) bool { return f(code) }

func TestRuleValidatorChecksLengthBeforeLookup(t *testing.T) {
	var looked []string
	v := RuleValidator{Codes: lookupFunc(func(c string) bool { looked = append(looked, c); return true })}
	for _, code := range []string{"SHORT", "WAYTOOLONGCODE", "HAPPYHRS"} {
		v.Valid(context.Background(), code)
	}
	if !slices.Equal(looked, []string{"HAPPYHRS"}) {
		t.Fatalf("lookups = %v, want only the valid-length code", looked)
	}
}

func TestCodeSet(t *testing.T) {
	var zero CodeSet // zero value is an empty, usable set
	if zero.Contains("HAPPYHRS") || zero.Len() != 0 {
		t.Fatal("zero-value CodeSet should be empty")
	}

	s := NewCodeSet([]string{"OLDCODE1"})
	s.Replace([]string{"NEWCODE1", "NEWCODE2"})
	if s.Contains("OLDCODE1") {
		t.Error("old code still present after Replace")
	}
	if !s.Contains("NEWCODE1") || s.Len() != 2 {
		t.Errorf("after Replace: Contains(NEWCODE1)=%v Len=%d", s.Contains("NEWCODE1"), s.Len())
	}
}

// Run with -race: readers and a writer hammer the set at the same time. Every
// read must see one whole set (A or B), never a mix or a torn map.
func TestCodeSetConcurrentReadsAndReplace(t *testing.T) {
	setA := []string{"AAAAAAAA"}
	setB := []string{"BBBBBBBB"}
	s := NewCodeSet(setA)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				a, b := s.Contains("AAAAAAAA"), s.Contains("BBBBBBBB")
				_ = a || b // each Contains sees a complete set
			}
		}()
	}
	for i := 0; i < 1000; i++ {
		if i%2 == 0 {
			s.Replace(setB)
		} else {
			s.Replace(setA)
		}
	}
	close(stop)
	wg.Wait()
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
// backend: Reload's rules must hold whatever the storage is.
type fakeSource struct {
	codes []string
	err   error
}

func (f fakeSource) Load(context.Context) ([]string, error) { return f.codes, f.err }

func TestReload(t *testing.T) {
	ctx := context.Background()
	set := &CodeSet{}

	if n, err := Reload(ctx, fakeSource{codes: []string{"HAPPYHRS", "FIFTYOFF"}}, set); err != nil || n != 2 {
		t.Fatalf("Reload = %d, %v; want 2, nil", n, err)
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
			if _, err := Reload(ctx, tt.src, set); !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if !set.Contains("HAPPYHRS") || set.Len() != 2 {
				t.Fatalf("old index lost after bad reload (len %d)", set.Len())
			}
		})
	}

	// A file that only has a header is empty too.
	p := filepath.Join(t.TempDir(), "header-only.txt")
	os.WriteFile(p, []byte("# 0 valid codes\n"), 0o644)
	if _, err := Reload(ctx, FileIndex{Path: p}, set); !errors.Is(err, ErrEmptyIndex) {
		t.Fatalf("header-only file: err = %v, want ErrEmptyIndex", err)
	}
}
