package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"
)

var retrieveAt = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

type retrieveEmbed struct {
	calls  [][]string
	model  string
	vector []float32
	err    error
}

func (e *retrieveEmbed) Embed(_ context.Context, texts []string) (string, [][]float32, error) {
	e.calls = append(e.calls, slices.Clone(texts))
	if e.err != nil {
		return "", nil, e.err
	}
	return e.model, [][]float32{e.vector}, nil
}

func goodEmbed() *retrieveEmbed {
	return &retrieveEmbed{model: "any-model", vector: oneHot(3)}
}

type retrieveStore struct {
	calls  int
	userID string
	near   []Candidate
	match  []Candidate
	limits []int
	asOf   []time.Time
	query  string
}

func (s *retrieveStore) InRetrieve(_ context.Context, userID string, fn func(RetrievalTx) error) error {
	s.calls++
	s.userID = userID
	return fn(s)
}

func (s *retrieveStore) NearText(_ context.Context, asOf time.Time, _ []float32, limit int) ([]Candidate, error) {
	s.limits = append(s.limits, limit)
	s.asOf = append(s.asOf, asOf)
	return s.near, nil
}

func (s *retrieveStore) MatchText(_ context.Context, asOf time.Time, query string, limit int) ([]Candidate, error) {
	s.limits = append(s.limits, limit)
	s.asOf = append(s.asOf, asOf)
	s.query = query
	return s.match, nil
}

type retrieveRerank struct {
	calls int
	got   []Candidate
	fn    func([]Candidate) ([]Candidate, error)
}

func (r *retrieveRerank) Rerank(_ context.Context, _ string, claims []Candidate) ([]Candidate, error) {
	r.calls++
	r.got = slices.Clone(claims)
	return r.fn(claims)
}

func cands(ids ...string) []Candidate {
	out := make([]Candidate, len(ids))
	for i, id := range ids {
		out[i] = Candidate{ID: id, Claim: "claim " + id}
	}
	return out
}

func ids(cs []Candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

func wantIDs(t *testing.T, got []Candidate, want ...string) {
	t.Helper()
	if !slices.Equal(ids(got), want) {
		t.Fatalf("ids = %v, want %v", ids(got), want)
	}
}

func TestRetrieveBlankQuerySkipsEmbed(t *testing.T) {
	embed, store := goodEmbed(), &retrieveStore{}
	if _, err := Retrieve(context.Background(), store, embed, nil, "u1", "  ", retrieveAt); err == nil {
		t.Fatal("blank query accepted")
	}
	if len(embed.calls) != 0 || store.calls != 0 {
		t.Fatalf("embed calls = %d, store calls = %d", len(embed.calls), store.calls)
	}
}

func TestRetrieveBlankUserSkipsEmbed(t *testing.T) {
	embed, store := goodEmbed(), &retrieveStore{}
	if _, err := Retrieve(context.Background(), store, embed, nil, " ", "tea", retrieveAt); err == nil {
		t.Fatal("blank user accepted")
	}
	if len(embed.calls) != 0 || store.calls != 0 {
		t.Fatalf("embed calls = %d, store calls = %d", len(embed.calls), store.calls)
	}
}

func TestRetrieveZeroAsOfSkipsEmbed(t *testing.T) {
	embed, store := goodEmbed(), &retrieveStore{}
	if _, err := Retrieve(context.Background(), store, embed, nil, "u1", "tea", time.Time{}); err == nil {
		t.Fatal("zero as of accepted")
	}
	if len(embed.calls) != 0 || store.calls != 0 {
		t.Fatalf("embed calls = %d, store calls = %d", len(embed.calls), store.calls)
	}
}

func TestRetrieveEmbedErrorSkipsStore(t *testing.T) {
	boom := fmt.Errorf("gateway: %w", ErrRetry)
	embed, store := &retrieveEmbed{err: boom}, &retrieveStore{}
	_, err := Retrieve(context.Background(), store, embed, nil, "u1", "tea", retrieveAt)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if store.calls != 0 {
		t.Fatalf("store calls = %d", store.calls)
	}
}

func TestRetrieveShortVectorSkipsStore(t *testing.T) {
	embed, store := &retrieveEmbed{model: "any-model", vector: []float32{1, 0, 0}}, &retrieveStore{}
	if _, err := Retrieve(context.Background(), store, embed, nil, "u1", "tea", retrieveAt); err == nil {
		t.Fatal("short vector accepted")
	}
	if store.calls != 0 {
		t.Fatalf("store calls = %d", store.calls)
	}
}

func TestRetrieveAsksBothListsOnceForFifty(t *testing.T) {
	embed := goodEmbed()
	store := &retrieveStore{near: cands("A", "B"), match: cands("A")}
	got, err := Retrieve(context.Background(), store, embed, nil, "u1", "tea at noon", retrieveAt)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs(t, got, "A", "B")
	if len(embed.calls) != 1 || !slices.Equal(embed.calls[0], []string{"tea at noon"}) {
		t.Fatalf("embed calls = %v", embed.calls)
	}
	if store.calls != 1 || store.userID != "u1" || store.query != "tea at noon" {
		t.Fatalf("store calls = %d, user = %q, query = %q", store.calls, store.userID, store.query)
	}
	if !slices.Equal(store.limits, []int{RetrieveCandidates, RetrieveCandidates}) {
		t.Fatalf("limits = %v", store.limits)
	}
	for _, at := range store.asOf {
		if !at.Equal(retrieveAt) {
			t.Fatalf("as of = %v", at)
		}
	}
	if got[0].Claim != "claim A" {
		t.Fatalf("claim = %q", got[0].Claim)
	}
}

func TestRetrieveBothListsOutrankOne(t *testing.T) {
	store := &retrieveStore{near: cands("A", "B", "C"), match: cands("X", "C")}
	got, err := Retrieve(context.Background(), store, goodEmbed(), nil, "u1", "tea", retrieveAt)
	if err != nil {
		t.Fatal(err)
	}
	// C: 1/63 + 1/62. A and X: 1/61 each, tie broken by ID. B: 1/62.
	wantIDs(t, got, "C", "A", "X", "B")
}

func TestRetrieveNilRerankerCapsFusedOrder(t *testing.T) {
	near := cands("a00", "a01", "a02", "a03", "a04", "a05", "a06", "a07", "a08", "a09", "a10", "a11")
	store := &retrieveStore{near: near}
	got, err := Retrieve(context.Background(), store, goodEmbed(), nil, "u1", "tea", retrieveAt)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs(t, got, "a00", "a01", "a02", "a03", "a04", "a05", "a06", "a07")
}

func TestRetrieveFusedListCapsAtFifty(t *testing.T) {
	var near, match []string
	for i := range 40 {
		near = append(near, fmt.Sprintf("n%02d", i))
		match = append(match, fmt.Sprintf("m%02d", i))
	}
	store := &retrieveStore{near: cands(near...), match: cands(match...)}
	rr := &retrieveRerank{fn: func(c []Candidate) ([]Candidate, error) { return c, nil }}
	if _, err := Retrieve(context.Background(), store, goodEmbed(), rr, "u1", "tea", retrieveAt); err != nil {
		t.Fatal(err)
	}
	if len(rr.got) != RetrieveCandidates {
		t.Fatalf("reranker saw %d, want %d", len(rr.got), RetrieveCandidates)
	}
}

func TestRetrieveRerankerReverseIsObeyed(t *testing.T) {
	store := &retrieveStore{near: cands("A", "B", "C")}
	rr := &retrieveRerank{fn: func(c []Candidate) ([]Candidate, error) {
		out := slices.Clone(c)
		slices.Reverse(out)
		return out, nil
	}}
	got, err := Retrieve(context.Background(), store, goodEmbed(), rr, "u1", "tea", retrieveAt)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs(t, rr.got, "A", "B", "C")
	wantIDs(t, got, "C", "B", "A")
}

func TestRetrieveRerankerErrorKeepsFusedOrder(t *testing.T) {
	store := &retrieveStore{near: cands("A", "B"), match: cands("B")}
	rr := &retrieveRerank{fn: func([]Candidate) ([]Candidate, error) { return nil, errors.New("sidecar down") }}
	got, err := Retrieve(context.Background(), store, goodEmbed(), rr, "u1", "tea", retrieveAt)
	if err != nil {
		t.Fatal(err)
	}
	if rr.calls != 1 {
		t.Fatalf("rerank calls = %d", rr.calls)
	}
	wantIDs(t, got, "B", "A")
}

func TestRetrieveRerankerEmptyKeepsFusedOrder(t *testing.T) {
	store := &retrieveStore{near: cands("A", "B")}
	rr := &retrieveRerank{fn: func([]Candidate) ([]Candidate, error) { return nil, nil }}
	got, err := Retrieve(context.Background(), store, goodEmbed(), rr, "u1", "tea", retrieveAt)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs(t, got, "A", "B")
}

func TestRetrieveRerankerOnlyUnknownIDsKeepsFusedOrder(t *testing.T) {
	store := &retrieveStore{near: cands("A", "B")}
	rr := &retrieveRerank{fn: func([]Candidate) ([]Candidate, error) {
		return []Candidate{{ID: "Z", Claim: "invented"}}, nil
	}}
	got, err := Retrieve(context.Background(), store, goodEmbed(), rr, "u1", "tea", retrieveAt)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs(t, got, "A", "B")
}

func TestRetrieveRerankerInventedIDIsDropped(t *testing.T) {
	store := &retrieveStore{near: cands("A", "B")}
	rr := &retrieveRerank{fn: func([]Candidate) ([]Candidate, error) {
		return []Candidate{{ID: "Z", Claim: "invented"}, {ID: "B"}, {ID: "B"}, {ID: "A"}}, nil
	}}
	got, err := Retrieve(context.Background(), store, goodEmbed(), rr, "u1", "tea", retrieveAt)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs(t, got, "B", "A")
	if got[0].Claim != "claim B" {
		t.Fatalf("claim = %q", got[0].Claim)
	}
}
