package promo

import (
	"context"
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
