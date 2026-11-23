package retrieval

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestFuseHandlesZeroOneAndManyLists(t *testing.T) {
	key := func(s string) string { return s }
	less := func(a, b string) bool { return a < b }
	for _, tc := range []struct {
		name  string
		lists [][]string
		want  []string
	}{
		{name: "zero", want: []string{}},
		{name: "one", lists: [][]string{{"b", "a"}}, want: []string{"b", "a"}},
		{name: "many with ties", lists: [][]string{{"b", "a"}, {"a", "b"}, {"c"}}, want: []string{"a", "b", "c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Fuse(tc.lists, key, less, func(a, _ string) string { return a }, 60, 50)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("Fuse() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRerankErrorKeepsFusedOrder(t *testing.T) {
	fused := []string{"a", "b"}
	got := Rerank(context.Background(), "query", fused, func(s string) string { return s }, func(context.Context, string, []string) ([]string, error) {
		return []string{"b"}, errors.New("ranker unavailable")
	})
	if !slices.Equal(got, fused) {
		t.Fatalf("Rerank() = %v, want %v", got, fused)
	}
}

func TestPackKeepsWholeItemsAndHonorsBudget(t *testing.T) {
	items := []string{"ab", "cde", "f"}
	got := Pack(items, 4, func(s string) int { return len(s) })
	if !slices.Equal(got, []string{"ab"}) {
		t.Fatalf("Pack() = %v, want [ab]", got)
	}
}

func TestPackZeroAndOversizedBudgets(t *testing.T) {
	items := []string{"a", "bc"}
	cost := func(s string) int { return len(s) }
	if got := Pack(items, 0, cost); len(got) != 0 {
		t.Fatalf("zero budget packed %v", got)
	}
	if got := Pack(items, 10, cost); !slices.Equal(got, items) {
		t.Fatalf("oversized budget packed %v, want %v", got, items)
	}
}
