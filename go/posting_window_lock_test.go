package isureal

import (
	"slices"
	"testing"
)

func TestPostingWindowLockRegionsAreSortedAndDeduplicated(t *testing.T) {
	regions := []string{"europe", "asia", "europe", "america"}
	got := normalizePostingWindowRegions(regions)
	want := []string{"america", "asia", "europe"}
	if !slices.Equal(got, want) {
		t.Fatalf("regions = %v, want %v", got, want)
	}
	if !slices.Equal(regions, []string{"europe", "asia", "europe", "america"}) {
		t.Fatalf("normalizePostingWindowRegions mutated input: %v", regions)
	}
}
