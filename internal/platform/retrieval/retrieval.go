package retrieval

import (
	"context"
	"sort"
)

// Fuse combines ranked lists with reciprocal rank fusion.
func Fuse[T any, K comparable](lists [][]T, key func(T) K, less func(T, T) bool, merge func(T, T) T, k, limit int) []T {
	type row struct {
		item  T
		score float64
	}
	rows := map[K]*row{}
	for _, list := range lists {
		for i, item := range list {
			id := key(item)
			r, ok := rows[id]
			if !ok {
				r = &row{item: item}
				rows[id] = r
			} else if merge != nil {
				r.item = merge(r.item, item)
			}
			r.score += 1 / float64(k+i+1)
		}
	}
	ranked := make([]*row, 0, len(rows))
	for _, r := range rows {
		ranked = append(ranked, r)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return less(ranked[i].item, ranked[j].item)
	})
	if limit >= 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]T, len(ranked))
	for i, r := range ranked {
		out[i] = r.item
	}
	return out
}

// Rerank keeps fused order when ranking fails or returns no known items.
func Rerank[T any, K comparable](ctx context.Context, query string, fused []T, key func(T) K, rank func(context.Context, string, []T) ([]T, error)) []T {
	if rank == nil || len(fused) == 0 {
		return fused
	}
	got, err := rank(ctx, query, fused)
	if err != nil || len(got) == 0 {
		return fused
	}
	known := make(map[K]T, len(fused))
	for _, item := range fused {
		known[key(item)] = item
	}
	seen := make(map[K]struct{}, len(got))
	out := make([]T, 0, len(got))
	for _, item := range got {
		id := key(item)
		original, ok := known[id]
		if !ok {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, original)
	}
	if len(out) == 0 {
		return fused
	}
	return out
}

// Pack returns the longest prefix of whole items whose costs fit the budget.
func Pack[T any](items []T, budget int, cost func(T) int) []T {
	if budget <= 0 {
		return []T{}
	}
	out := make([]T, 0, len(items))
	for _, item := range items {
		itemCost := cost(item)
		if itemCost > budget {
			break
		}
		out = append(out, item)
		budget -= itemCost
	}
	return out
}
