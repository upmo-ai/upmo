package decisions

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

const rerankUser = "018f3b2c-7a10-7c4a-8e11-111111111111"

var rerankHops = []Hop{
	{Position: 1, Adapter: "openai", Model: "small", Weight: 1, Retry: 0},
}

func TestRerankRejectsBlankQueryBeforeOrder(t *testing.T) {
	book := &rerankGates{}
	rank := &rerankRanker{orders: [][]string{{"chunk_a", "chunk_b"}}}
	err := Rerank(context.Background(), book, rerankAccess(), rerankHops, 100, []Ranking{{
		Query:    " \t\n ",
		Passages: rerankPassages(),
	}}, rank)
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("error = %v", err)
	}
	if rank.calls != 0 {
		t.Fatalf("ranker calls = %d", rank.calls)
	}
}

func TestRerankRejectsRelevanceFourBeforeOrder(t *testing.T) {
	book := &rerankGates{}
	rank := &rerankRanker{orders: [][]string{{"chunk_a", "chunk_b"}}}
	passages := rerankPassages()
	passages[0].Relevance = 4
	err := Rerank(context.Background(), book, rerankAccess(), rerankHops, 100, []Ranking{{
		Query:    "Find the answer",
		Passages: passages,
	}}, rank)
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("error = %v", err)
	}
	if rank.calls != 0 {
		t.Fatalf("ranker calls = %d", rank.calls)
	}
}

func TestRerankRejectsAllZeroRelevanceBeforeOrder(t *testing.T) {
	book := &rerankGates{}
	rank := &rerankRanker{orders: [][]string{{"chunk_a", "chunk_b"}}}
	passages := rerankPassages()
	passages[0].Relevance = 0
	err := Rerank(context.Background(), book, rerankAccess(), rerankHops, 100, []Ranking{{
		Query:    "Find the answer",
		Passages: passages,
	}}, rank)
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("error = %v", err)
	}
	if rank.calls != 0 {
		t.Fatalf("ranker calls = %d", rank.calls)
	}
}

func TestRerankRefusesCallerWithoutSettingsManage(t *testing.T) {
	book := &rerankGates{}
	rank := &rerankRanker{orders: [][]string{{"chunk_a", "chunk_b"}}}
	access := Access{UserID: rerankUser, Allow: "task:edit"}
	err := Rerank(context.Background(), book, access, rerankHops, 100, rerankRankings(), rank)
	if !errors.Is(err, ErrAccess) {
		t.Fatalf("error = %v", err)
	}
	if rank.calls != 0 {
		t.Fatalf("ranker calls = %d", rank.calls)
	}
}

func TestRerankSkipsOrderForStoredFingerprint(t *testing.T) {
	fingerprint, err := Fingerprint(Config{Name: RouteRerank, Kind: KindRank, Hops: rerankHops})
	if err != nil {
		t.Fatal(err)
	}
	book := &rerankGates{fingerprint: fingerprint, accepted: true}
	rank := &rerankRanker{orders: [][]string{{"chunk_a", "chunk_b"}}}
	if err := Rerank(context.Background(), book, rerankAccess(), rerankHops, 100, rerankRankings(), rank); err != nil {
		t.Fatal(err)
	}
	if rank.calls != 0 {
		t.Fatalf("ranker calls = %d", rank.calls)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestRerankRejectsNilRankerForNewFingerprint(t *testing.T) {
	book := &rerankGates{}
	err := Rerank(context.Background(), book, rerankAccess(), rerankHops, 100, rerankRankings(), nil)
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestAcceptRejectsRankKindWithoutRunning(t *testing.T) {
	book := &rerankGates{}
	run := &rerankRunner{}
	err := Accept(
		context.Background(),
		book,
		rerankAccess(),
		Config{Name: RouteRerank, Kind: KindRank, Hops: rerankHops},
		100,
		[]Example{{Prompt: "Rank these"}},
		run,
	)
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("error = %v", err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestRerankStoresPerfectRanking(t *testing.T) {
	book := &rerankGates{}
	rank := &rerankRanker{orders: [][]string{{"chunk_a", "chunk_b"}}}
	if err := Rerank(context.Background(), book, rerankAccess(), rerankHops, 100, rerankRankings(), rank); err != nil {
		t.Fatal(err)
	}
	if len(book.saved) != 1 {
		t.Fatalf("saved = %d", len(book.saved))
	}
	got := book.saved[0]
	if got.Route != RouteRerank || got.Kind != KindRank || got.Hit != 100 || got.Total != 100 || got.Bar != 100 {
		t.Fatalf("gate = %+v", got)
	}
}

func TestRerankScoresReversedPairAtSixtyThree(t *testing.T) {
	t.Run("passes bar sixty-three", func(t *testing.T) {
		book := &rerankGates{}
		rank := &rerankRanker{orders: [][]string{{"chunk_b", "chunk_a"}}}
		if err := Rerank(context.Background(), book, rerankAccess(), rerankHops, 63, rerankRankings(), rank); err != nil {
			t.Fatal(err)
		}
		if len(book.saved) != 1 {
			t.Fatalf("saved = %d", len(book.saved))
		}
		if got := book.saved[0]; got.Hit != 63 || got.Total != 100 || got.Bar != 63 {
			t.Fatalf("gate = %+v", got)
		}
	})

	t.Run("fails bar sixty-four", func(t *testing.T) {
		book := &rerankGates{}
		rank := &rerankRanker{orders: [][]string{{"chunk_b", "chunk_a"}}}
		err := Rerank(context.Background(), book, rerankAccess(), rerankHops, 64, rerankRankings(), rank)
		if !errors.Is(err, ErrGate) {
			t.Fatalf("error = %v", err)
		}
		if len(book.saved) != 0 {
			t.Fatalf("saved = %+v", book.saved)
		}
	})
}

func TestRerankRoundsMeanNDCGOnce(t *testing.T) {
	book := &rerankGates{}
	rank := &rerankRanker{orders: [][]string{
		{"chunk_a", "chunk_b"},
		{"chunk_b", "chunk_a"},
	}}
	rankings := []Ranking{
		{Query: "First answer", Passages: rerankPassages()},
		{Query: "Second answer", Passages: rerankPassages()},
	}
	if err := Rerank(context.Background(), book, rerankAccess(), rerankHops, 82, rankings, rank); err != nil {
		t.Fatal(err)
	}
	if len(book.saved) != 1 {
		t.Fatalf("saved = %d", len(book.saved))
	}
	if got := book.saved[0]; got.Hit != 82 || got.Total != 100 || got.Bar != 82 {
		t.Fatalf("gate = %+v", got)
	}
}

func TestRerankRejectsUnknownIDWithoutSaving(t *testing.T) {
	book := &rerankGates{}
	rank := &rerankRanker{orders: [][]string{{"chunk_a", "chunk_c"}}}
	err := Rerank(context.Background(), book, rerankAccess(), rerankHops, 100, rerankRankings(), rank)
	if !errors.Is(err, ErrGate) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestRerankReturnsOrderErrorWithoutSaving(t *testing.T) {
	want := errors.New("order failed")
	book := &rerankGates{}
	rank := &rerankRanker{errs: []error{want}}
	err := Rerank(context.Background(), book, rerankAccess(), rerankHops, 100, rerankRankings(), rank)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestRerankHidesRelevanceWithoutMutatingCaller(t *testing.T) {
	book := &rerankGates{}
	rank := &rerankRanker{orders: [][]string{{"chunk_a", "chunk_b"}}}
	rankings := rerankRankings()
	want := append([]Passage(nil), rankings[0].Passages...)
	if err := Rerank(context.Background(), book, rerankAccess(), rerankHops, 100, rankings, rank); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rankings[0].Passages, want) {
		t.Fatalf("caller passages = %+v", rankings[0].Passages)
	}
	if len(rank.passages) != 1 {
		t.Fatalf("ranker passages = %+v", rank.passages)
	}
	for _, passage := range rank.passages[0] {
		if passage.Relevance != 0 {
			t.Fatalf("ranker passage = %+v", passage)
		}
	}
}

func rerankRankings() []Ranking {
	return []Ranking{{
		Query:    "Find the answer",
		Passages: rerankPassages(),
	}}
}

func rerankPassages() []Passage {
	return []Passage{
		{ID: "chunk_a", Text: "Relevant text", Relevance: 1},
		{ID: "chunk_b", Text: "Other text", Relevance: 0},
	}
}

func rerankAccess() Access {
	return Access{UserID: rerankUser, Allow: "settings:manage"}
}

type rerankGates struct {
	fingerprint string
	accepted    bool
	saved       []Gate
}

func (g *rerankGates) Accepted(context.Context, Access, string) (string, bool, error) {
	return g.fingerprint, g.accepted, nil
}

func (g *rerankGates) Save(_ context.Context, _ Access, gate Gate) error {
	g.saved = append(g.saved, gate)
	return nil
}

type rerankRanker struct {
	orders   [][]string
	errs     []error
	calls    int
	queries  []string
	passages [][]Passage
}

func (r *rerankRanker) Order(_ context.Context, query string, passages []Passage) ([]string, error) {
	call := r.calls
	r.calls++
	r.queries = append(r.queries, query)
	r.passages = append(r.passages, append([]Passage(nil), passages...))
	if call < len(r.errs) && r.errs[call] != nil {
		return nil, r.errs[call]
	}
	if call < len(r.orders) {
		return append([]string(nil), r.orders[call]...), nil
	}
	return nil, nil
}

type rerankRunner struct {
	calls int
}

func (r *rerankRunner) Decide(context.Context, string, []string) (bool, string, error) {
	r.calls++
	return false, "", nil
}
