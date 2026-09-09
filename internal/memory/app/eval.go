package app

import (
	"context"
	"time"
)

// Question is one canary item. It is answered only when retrieval returns
// Expect. A model saying that it answered does not count.
type Question struct {
	Query  string
	Expect string
}

// Score is the canary result. Tokens estimates the query and the claims
// that were read, including questions that missed, so a noisy list costs
// more per correct answer.
type Score struct {
	Asked    int
	Answered int
	Tokens   int
}

// TokensPerAnswered is the total tokens divided by the number answered.
// It is zero when nothing was answered.
func (s Score) TokensPerAnswered() int {
	if s.Answered == 0 {
		return 0
	}
	return s.Tokens / s.Answered
}

// Evaluate runs each question through retrieval. A failure returns no
// score, so a partial run cannot look finished.
func Evaluate(
	ctx context.Context,
	store RetrievalStore,
	embedder Embedder,
	reranker Reranker,
	userID string,
	questions []Question,
	asOf time.Time,
) (Score, error) {
	var score Score
	for _, question := range questions {
		hits, err := Retrieve(ctx, store, embedder, reranker, userID, question.Query, asOf)
		if err != nil {
			return Score{}, err
		}
		score.Asked++
		score.Tokens += textTokens(question.Query)
		found := false
		for _, hit := range hits {
			score.Tokens += textTokens(hit.Claim)
			if hit.Claim == question.Expect {
				found = true
			}
		}
		if found {
			score.Answered++
		}
	}
	return score, nil
}

// textTokens uses the profile budget's unit: four bytes are one token,
// rounded up so a short line still counts.
func textTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}
