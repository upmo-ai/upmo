package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type reindexEmbed struct {
	calls   int
	batches [][]string
	model   string
	vectors [][]float32
	err     error
}

func (e *reindexEmbed) Embed(_ context.Context, texts []string) (string, [][]float32, error) {
	e.calls++
	e.batches = append(e.batches, append([]string(nil), texts...))
	if e.err != nil {
		return "", nil, e.err
	}
	if e.vectors != nil {
		return e.model, e.vectors, nil
	}
	vectors := make([][]float32, len(texts))
	for i := range vectors {
		vectors[i] = oneHot(i)
	}
	return e.model, vectors, nil
}

type storedVector struct {
	model  string
	vector []float32
}

type reindexStore struct {
	rows       []Embedded
	pin        string
	vectors    map[string]storedVector
	inCalls    int
	clearCalls int
	setCalls   int
}

type reindexTx struct {
	store   *reindexStore
	rows    []Embedded
	pin     string
	vectors map[string]storedVector
}

func (s *reindexStore) InReindex(_ context.Context, _ string, fn func(ReindexTx) error) error {
	s.inCalls++
	tx := &reindexTx{
		store:   s,
		rows:    append([]Embedded(nil), s.rows...),
		pin:     s.pin,
		vectors: cloneVectors(s.vectors),
	}
	if err := fn(tx); err != nil {
		return err
	}
	s.rows = tx.rows
	s.pin = tx.pin
	s.vectors = tx.vectors
	return nil
}

func (t *reindexTx) Embedded(context.Context) ([]Embedded, error) {
	return append([]Embedded(nil), t.rows...), nil
}

func (t *reindexTx) ClearPin(context.Context) error {
	t.store.clearCalls++
	t.pin = ""
	return nil
}

func (t *reindexTx) SetVector(_ context.Context, id, model string, vector []float32) error {
	t.store.setCalls++
	if t.pin != "" && t.pin != model {
		return fmt.Errorf("mixed embedding model")
	}
	t.pin = model
	for i := range t.rows {
		if t.rows[i].ID == id {
			t.rows[i].Model = model
			if t.vectors == nil {
				t.vectors = make(map[string]storedVector)
			}
			t.vectors[id] = storedVector{model: model, vector: append([]float32(nil), vector...)}
			return nil
		}
	}
	return fmt.Errorf("missing fact")
}

func cloneVectors(src map[string]storedVector) map[string]storedVector {
	if src == nil {
		return nil
	}
	dst := make(map[string]storedVector, len(src))
	for id, stored := range src {
		stored.vector = append([]float32(nil), stored.vector...)
		dst[id] = stored
	}
	return dst
}

func TestReindexAlreadyOnTargetModel(t *testing.T) {
	store := &reindexStore{rows: []Embedded{{ID: "fact-1", Claim: "Ada prefers tea", Model: "model-b"}}, pin: "model-b"}
	embed := &reindexEmbed{model: "model-b"}

	n, err := Reindex(context.Background(), store, embed, testUser, "model-b")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || embed.calls != 0 || store.clearCalls != 0 || store.pin != "model-b" {
		t.Fatalf("n=%d embed=%d clear=%d pin=%q", n, embed.calls, store.clearCalls, store.pin)
	}
}

func TestReindexOneOldModelFact(t *testing.T) {
	store := &reindexStore{rows: []Embedded{{ID: "fact-1", Claim: "Ada prefers tea", Model: "model-a"}}, pin: "model-a"}
	embed := &reindexEmbed{model: "model-b"}

	n, err := Reindex(context.Background(), store, embed, testUser, "model-b")
	if err != nil {
		t.Fatal(err)
	}
	stored, ok := store.vectors["fact-1"]
	if n != 1 || embed.calls != 1 || store.clearCalls != 1 || !ok || stored.model != "model-b" || store.pin != "model-b" {
		t.Fatalf("n=%d embed=%d clear=%d stored=%+v ok=%v pin=%q", n, embed.calls, store.clearCalls, stored, ok, store.pin)
	}
	if len(embed.batches) != 1 || len(embed.batches[0]) != 1 || embed.batches[0][0] != "Ada prefers tea" {
		t.Fatalf("batches = %v", embed.batches)
	}
}

func TestReindexIncludesForgottenFact(t *testing.T) {
	store := &reindexStore{rows: []Embedded{{ID: "forgotten", Claim: "Ada used to prefer tea", Model: "model-a"}}, pin: "model-a"}
	embed := &reindexEmbed{model: "model-b"}

	n, err := Reindex(context.Background(), store, embed, testUser, "model-b")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || store.vectors["forgotten"].model != "model-b" {
		t.Fatalf("n=%d vectors=%v", n, store.vectors)
	}
}

func TestReindexBatchesSixtyFiveFacts(t *testing.T) {
	rows := make([]Embedded, 65)
	for i := range rows {
		rows[i] = Embedded{ID: fmt.Sprintf("fact-%d", i), Claim: fmt.Sprintf("claim %d", i), Model: "model-a"}
	}
	store := &reindexStore{rows: rows, pin: "model-a"}
	embed := &reindexEmbed{model: "model-b"}

	n, err := Reindex(context.Background(), store, embed, testUser, "model-b")
	if err != nil {
		t.Fatal(err)
	}
	if n != 65 || embed.calls != 2 || len(embed.batches[0]) != ReindexBatch || len(embed.batches[1]) != 1 {
		t.Fatalf("n=%d calls=%d batch sizes=%d,%d", n, embed.calls, len(embed.batches[0]), len(embed.batches[1]))
	}
}

func TestReindexEmbedderErrorRollsBack(t *testing.T) {
	retry := errors.Join(ErrRetry, errors.New("timeout"))
	store := &reindexStore{rows: []Embedded{{ID: "fact-1", Claim: "Ada prefers tea", Model: "model-a"}}, pin: "model-a"}
	embed := &reindexEmbed{err: retry}

	n, err := Reindex(context.Background(), store, embed, testUser, "model-b")
	if n != 0 || !errors.Is(err, ErrRetry) {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if store.pin != "model-a" || len(store.vectors) != 0 {
		t.Fatalf("pin=%q vectors=%v", store.pin, store.vectors)
	}
}

func TestReindexRejectsDifferentModelBeforeSet(t *testing.T) {
	store := &reindexStore{rows: []Embedded{{ID: "fact-1", Claim: "Ada prefers tea", Model: "model-a"}}, pin: "model-a"}
	embed := &reindexEmbed{model: "model-c"}

	n, err := Reindex(context.Background(), store, embed, testUser, "model-b")
	if n != 0 || err == nil || store.setCalls != 0 {
		t.Fatalf("n=%d err=%v set=%d", n, err, store.setCalls)
	}
	if store.pin != "model-a" {
		t.Fatalf("pin = %q", store.pin)
	}
}

func TestReindexRejectsShortVectorBeforeSet(t *testing.T) {
	store := &reindexStore{rows: []Embedded{{ID: "fact-1", Claim: "Ada prefers tea", Model: "model-a"}}, pin: "model-a"}
	embed := &reindexEmbed{model: "model-b", vectors: [][]float32{{1}}}

	n, err := Reindex(context.Background(), store, embed, testUser, "model-b")
	if n != 0 || err == nil || store.setCalls != 0 {
		t.Fatalf("n=%d err=%v set=%d", n, err, store.setCalls)
	}
	if store.pin != "model-a" {
		t.Fatalf("pin = %q", store.pin)
	}
}

func TestReindexRejectsBlankModelBeforeCalls(t *testing.T) {
	store := &reindexStore{}
	embed := &reindexEmbed{}

	n, err := Reindex(context.Background(), store, embed, testUser, " \t")
	if n != 0 || err == nil || store.inCalls != 0 || embed.calls != 0 {
		t.Fatalf("n=%d err=%v store=%d embed=%d", n, err, store.inCalls, embed.calls)
	}
}

func TestReindexRejectsBlankUserBeforeCalls(t *testing.T) {
	store := &reindexStore{}
	embed := &reindexEmbed{}

	n, err := Reindex(context.Background(), store, embed, " ", "model-b")
	if n != 0 || err == nil || store.inCalls != 0 || embed.calls != 0 {
		t.Fatalf("n=%d err=%v store=%d embed=%d", n, err, store.inCalls, embed.calls)
	}
}
