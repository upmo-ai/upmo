package decisions

import (
	"context"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// RouteRerank is the route scored on ranking quality.
const RouteRerank = "rerank"

var rerankIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// Passage is one offered text and its labeled relevance.
type Passage struct {
	ID        string
	Text      string
	Relevance int
}

// Ranking is one query and the passages to order for it.
type Ranking struct {
	Query    string
	Passages []Passage
}

// Ranker orders offered passages by identifier.
type Ranker interface {
	Order(ctx context.Context, query string, passages []Passage) (ids []string, err error)
}

// Rerank accepts a rerank config when its mean NDCG meets the bar.
func Rerank(ctx context.Context, book Gates, access Access, hops []Hop, bar int, rankings []Ranking, rank Ranker) error {
	checked, err := validateRankings(rankings)
	if err != nil {
		return err
	}

	cfg := Config{
		Name: RouteRerank,
		Kind: KindRank,
		Hops: hops,
	}
	fingerprint, unchanged, err := prepared(ctx, book, access, cfg)
	if err != nil {
		return err
	}
	if unchanged {
		return nil
	}
	if rank == nil {
		return ErrConfig
	}

	var totalNDCG float64
	for _, ranking := range checked {
		offered := append([]Passage(nil), ranking.Passages...)
		for i := range offered {
			offered[i].Relevance = 0
		}
		ids, err := rank.Order(ctx, ranking.Query, offered)
		if err != nil {
			return err
		}
		score, err := ndcg(ranking.Passages, ids)
		if err != nil {
			return err
		}
		totalNDCG += score
	}

	percent := int(math.Round(totalNDCG / float64(len(checked)) * 100))
	return keep(ctx, book, access, cfg, fingerprint, bar, percent, 100)
}

func validateRankings(rankings []Ranking) ([]Ranking, error) {
	if len(rankings) == 0 {
		return nil, ErrConfig
	}

	checked := make([]Ranking, len(rankings))
	for i, ranking := range rankings {
		query := strings.TrimSpace(ranking.Query)
		if runes := utf8.RuneCountInString(query); runes < 1 || runes > 200 {
			return nil, ErrConfig
		}
		if len(ranking.Passages) < 2 || len(ranking.Passages) > 8 {
			return nil, ErrConfig
		}

		passages := make([]Passage, len(ranking.Passages))
		ids := make(map[string]struct{}, len(ranking.Passages))
		hasRelevant := false
		for j, passage := range ranking.Passages {
			id := strings.TrimSpace(passage.ID)
			text := strings.TrimSpace(passage.Text)
			if !rerankIDPattern.MatchString(id) {
				return nil, ErrConfig
			}
			if _, duplicate := ids[id]; duplicate {
				return nil, ErrConfig
			}
			if runes := utf8.RuneCountInString(text); runes < 1 || runes > 400 {
				return nil, ErrConfig
			}
			if passage.Relevance < 0 || passage.Relevance > 3 {
				return nil, ErrConfig
			}

			ids[id] = struct{}{}
			hasRelevant = hasRelevant || passage.Relevance > 0
			passages[j] = Passage{
				ID:        id,
				Text:      text,
				Relevance: passage.Relevance,
			}
		}
		if !hasRelevant {
			return nil, ErrConfig
		}
		checked[i] = Ranking{Query: query, Passages: passages}
	}
	return checked, nil
}

func ndcg(passages []Passage, ids []string) (float64, error) {
	if len(ids) != len(passages) {
		return 0, ErrGate
	}

	byID := make(map[string]Passage, len(passages))
	for _, passage := range passages {
		byID[passage.ID] = passage
	}
	ordered := make([]Passage, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for i, id := range ids {
		passage, offered := byID[id]
		if !offered {
			return 0, ErrGate
		}
		if _, duplicate := seen[id]; duplicate {
			return 0, ErrGate
		}
		seen[id] = struct{}{}
		ordered[i] = passage
	}

	ideal := append([]Passage(nil), passages...)
	sort.SliceStable(ideal, func(i, j int) bool {
		return ideal[i].Relevance > ideal[j].Relevance
	})
	return dcg(ordered) / dcg(ideal), nil
}

func dcg(passages []Passage) float64 {
	var score float64
	for i, passage := range passages {
		gain := (1 << passage.Relevance) - 1
		score += float64(gain) / math.Log2(float64(i+2))
	}
	return score
}
