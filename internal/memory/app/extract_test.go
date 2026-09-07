package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/memory/domain"
)

const (
	testUser = "018f3b2c-7a10-7c4a-8e11-111111111111"
	testID   = "018f3b2c-7a10-7c4a-8e11-333333333333"
)

func testSource(text string) Source {
	return Source{
		UserID: testUser,
		Kind:   "chat",
		ID:     testID,
		Seq:    1,
		Text:   text,
		At:     time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
	}
}

type scriptPropose struct {
	calls  int
	prompt string
	bodies []string
	err    error
}

func (s *scriptPropose) Propose(_ context.Context, prompt string) (string, error) {
	s.calls++
	s.prompt = prompt
	if s.err != nil {
		return "", s.err
	}
	i := s.calls - 1
	if i >= len(s.bodies) {
		return s.bodies[len(s.bodies)-1], nil
	}
	return s.bodies[i], nil
}

type scriptEmbed struct {
	calls int
	texts []string
	model string
	vecs  [][]float32
	err   error
}

func (s *scriptEmbed) Embed(_ context.Context, texts []string) (string, [][]float32, error) {
	s.calls++
	s.texts = texts
	if s.err != nil {
		return "", nil, s.err
	}
	if s.vecs != nil {
		return s.model, s.vecs, nil
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = oneHot(i)
	}
	model := s.model
	if model == "" {
		model = "model-a"
	}
	return model, out, nil
}

func oneHot(i int) []float32 {
	v := make([]float32, domain.Dim)
	v[i%domain.Dim] = 1
	return v
}

type memStore struct {
	done       bool
	active     [][32]byte
	forgotten  [][32]byte
	added      []Fact
	confirms   int
	confirmIDs []string
	nearAction string
	nearID     string

	recent      int
	recentCalls int
	recentKind  string
	recentID    string
	recentSince time.Time
	records     int
}

func (m *memStore) InTx(_ context.Context, _ string, fn func(Tx) error) error {
	return fn(m)
}

func (m *memStore) Already(context.Context, Key) (bool, error) { return m.done, nil }

func (m *memStore) Record(context.Context, Key, time.Time) error {
	m.done = true
	m.records++
	return nil
}

func (m *memStore) Recent(_ context.Context, kind, id string, since time.Time) (int, error) {
	m.recentCalls++
	m.recentKind = kind
	m.recentID = id
	m.recentSince = since
	return m.recent, nil
}

func (m *memStore) Hashes(context.Context) ([][32]byte, [][32]byte, error) {
	return m.active, m.forgotten, nil
}

func (m *memStore) Confirm(context.Context, [32]byte, time.Time) error {
	m.confirms++
	return nil
}

func (m *memStore) ConfirmID(_ context.Context, id string, _ time.Time) error {
	m.confirmIDs = append(m.confirmIDs, id)
	return nil
}

func (m *memStore) Near(context.Context, []float32) (string, string, error) {
	if m.nearAction == "" {
		return "insert", "", nil
	}
	return m.nearAction, m.nearID, nil
}

func (m *memStore) Add(_ context.Context, fact Fact) error {
	m.added = append(m.added, fact)
	return nil
}

func TestExtractFencesAndRedactsBeforePropose(t *testing.T) {
	secret := "abcDEF123456"
	store := &memStore{}
	prop := &scriptPropose{bodies: []string{`[]`}}
	src := testSource("Authorization header is Bearer " + secret + " and I prefer tea")
	rep, err := Extract(context.Background(), store, prop, &scriptEmbed{}, src)
	if err != nil {
		t.Fatal(err)
	}
	if prop.calls != 1 || !store.done || rep.Inserted != 0 {
		t.Fatalf("calls=%d done=%v report=%+v", prop.calls, store.done, rep)
	}
	if !strings.Contains(prop.prompt, `<untrust source="user">`) {
		t.Fatalf("prompt was not fenced: %s", prop.prompt)
	}
	if strings.Contains(prop.prompt, secret) {
		t.Fatal("prompt contained the secret")
	}
	if !strings.Contains(prop.prompt, "2026-10-30T12:00:00Z") {
		t.Fatal("prompt omitted the source time")
	}
}

func TestExtractDropsUngroundedBeforeEmbed(t *testing.T) {
	store := &memStore{}
	prop := &scriptPropose{bodies: []string{`[{"claim":"Zebras invented calendars","changes_behavior":true}]`}}
	emb := &scriptEmbed{}
	rep, err := Extract(context.Background(), store, prop, emb, testSource("Ada prefers tea every morning"))
	if err != nil {
		t.Fatal(err)
	}
	if emb.calls != 0 || rep.Dropped != 1 || len(store.added) != 0 || !store.done {
		t.Fatalf("embed=%d report=%+v added=%d", emb.calls, rep, len(store.added))
	}
}

func TestExtractDropsGeneralBeforeEmbed(t *testing.T) {
	store := &memStore{}
	prop := &scriptPropose{bodies: []string{`[{"claim":"Ada prefers tea","changes_behavior":false}]`}}
	emb := &scriptEmbed{}
	rep, err := Extract(context.Background(), store, prop, emb, testSource("Ada prefers tea every morning"))
	if err != nil {
		t.Fatal(err)
	}
	if emb.calls != 0 || rep.Dropped != 1 {
		t.Fatalf("embed=%d report=%+v", emb.calls, rep)
	}
}

func TestExtractRejectsASixthFactWithoutEmbed(t *testing.T) {
	six := `[` + strings.TrimRight(strings.Repeat(`{"claim":"Ada prefers tea","changes_behavior":true},`, 6), ",") + `]`
	store := &memStore{}
	prop := &scriptPropose{bodies: []string{six, six}}
	emb := &scriptEmbed{}
	rep, err := Extract(context.Background(), store, prop, emb, testSource("Ada prefers tea every morning"))
	if err != nil {
		t.Fatal(err)
	}
	if prop.calls != 2 || emb.calls != 0 || !store.done || rep.Inserted != 0 {
		t.Fatalf("calls=%d embed=%d done=%v report=%+v", prop.calls, emb.calls, store.done, rep)
	}
}

func TestExtractEmbedsFive(t *testing.T) {
	body := `[
		{"claim":"Ada prefers tea","changes_behavior":true},
		{"claim":"Ada prefers coffee","changes_behavior":true},
		{"claim":"Ada prefers juice","changes_behavior":true},
		{"claim":"Ada prefers bread","changes_behavior":true},
		{"claim":"Ada prefers rice","changes_behavior":true}
	]`
	store := &memStore{}
	prop := &scriptPropose{bodies: []string{body}}
	emb := &scriptEmbed{model: "model-a"}
	rep, err := Extract(context.Background(), store, prop, emb, testSource("Ada prefers tea coffee juice bread and rice every morning"))
	if err != nil {
		t.Fatal(err)
	}
	if emb.calls != 1 || len(emb.texts) != 5 || rep.Inserted != 5 || len(store.added) != 5 {
		t.Fatalf("embed=%d texts=%d report=%+v", emb.calls, len(emb.texts), rep)
	}
	if store.added[0].Model != "model-a" {
		t.Fatalf("model = %s", store.added[0].Model)
	}
}

func TestExtractRetryableEmbedWritesNothing(t *testing.T) {
	store := &memStore{}
	prop := &scriptPropose{bodies: []string{`[{"claim":"Ada prefers tea","changes_behavior":true}]`}}
	emb := &scriptEmbed{err: errors.Join(ErrRetry, errors.New("gateway timeout"))}
	_, err := Extract(context.Background(), store, prop, emb, testSource("Ada prefers tea every morning"))
	if !errors.Is(err, ErrRetry) {
		t.Fatalf("err = %v", err)
	}
	if store.done || len(store.added) != 0 {
		t.Fatal("retry stored a run")
	}
}

func TestExtractPersistentEmbedWritesNothing(t *testing.T) {
	store := &memStore{}
	prop := &scriptPropose{bodies: []string{`[{"claim":"Ada prefers tea","changes_behavior":true}]`}}
	emb := &scriptEmbed{model: "model-a", vecs: [][]float32{{1, 0}}}
	_, err := Extract(context.Background(), store, prop, emb, testSource("Ada prefers tea every morning"))
	if err == nil || errors.Is(err, ErrRetry) {
		t.Fatalf("err = %v", err)
	}
	if store.done || len(store.added) != 0 {
		t.Fatal("bad vector stored a run")
	}
}

func TestExtractDuplicateSkipsPropose(t *testing.T) {
	store := &memStore{}
	prop := &scriptPropose{bodies: []string{`[{"claim":"Ada prefers tea","changes_behavior":true}]`}}
	src := testSource("Ada prefers tea every morning")
	if _, err := Extract(context.Background(), store, prop, &scriptEmbed{model: "model-a"}, src); err != nil {
		t.Fatal(err)
	}
	rep, err := Extract(context.Background(), store, prop, &scriptEmbed{model: "model-a"}, src)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Duplicate || prop.calls != 1 || len(store.added) != 1 {
		t.Fatalf("report=%+v calls=%d added=%d", rep, prop.calls, len(store.added))
	}
}

func TestExtractShortTextSkips(t *testing.T) {
	store := &memStore{}
	prop := &scriptPropose{bodies: []string{`[]`}}
	rep, err := Extract(context.Background(), store, prop, &scriptEmbed{}, testSource("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Skipped || prop.calls != 0 || !store.done {
		t.Fatalf("report=%+v calls=%d", rep, prop.calls)
	}
}

func TestExtractHashReinforceDoesNotEmbed(t *testing.T) {
	claim := "Ada prefers tea"
	store := &memStore{active: [][32]byte{domain.ClaimHash(claim)}}
	prop := &scriptPropose{bodies: []string{`[{"claim":"Ada prefers tea","changes_behavior":true}]`}}
	emb := &scriptEmbed{}
	rep, err := Extract(context.Background(), store, prop, emb, testSource("Ada prefers tea every morning"))
	if err != nil {
		t.Fatal(err)
	}
	if emb.calls != 0 || rep.Reinforced != 1 || store.confirms != 1 || len(store.added) != 0 {
		t.Fatalf("embed=%d report=%+v confirms=%d", emb.calls, rep, store.confirms)
	}
}

func TestExtractForgottenHashDoesNotEmbed(t *testing.T) {
	claim := "Ada prefers tea"
	store := &memStore{forgotten: [][32]byte{domain.ClaimHash(claim)}}
	prop := &scriptPropose{bodies: []string{`[{"claim":"Ada prefers tea","changes_behavior":true}]`}}
	emb := &scriptEmbed{}
	rep, err := Extract(context.Background(), store, prop, emb, testSource("Ada prefers tea every morning"))
	if err != nil {
		t.Fatal(err)
	}
	if emb.calls != 0 || rep.Dropped != 1 || len(store.added) != 0 {
		t.Fatalf("embed=%d report=%+v", emb.calls, rep)
	}
}

func TestExtractNearReinforceDoesNotInsert(t *testing.T) {
	store := &memStore{nearAction: "reinforce", nearID: "fact-1"}
	prop := &scriptPropose{bodies: []string{`[{"claim":"Ada prefers tea","changes_behavior":true}]`}}
	rep, err := Extract(context.Background(), store, prop, &scriptEmbed{model: "model-a"}, testSource("Ada prefers tea every morning"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Reinforced != 1 || len(store.added) != 0 || len(store.confirmIDs) != 1 || store.confirmIDs[0] != "fact-1" {
		t.Fatalf("report=%+v ids=%v", rep, store.confirmIDs)
	}
}

func TestExtractForgottenNearDoesNotInsert(t *testing.T) {
	store := &memStore{nearAction: "drop", nearID: "gone"}
	prop := &scriptPropose{bodies: []string{`[{"claim":"Ada prefers tea","changes_behavior":true}]`}}
	rep, err := Extract(context.Background(), store, prop, &scriptEmbed{model: "model-a"}, testSource("Ada prefers tea every morning"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Dropped != 1 || len(store.added) != 0 {
		t.Fatalf("report=%+v added=%d", rep, len(store.added))
	}
}

func TestExtractLimitedSkipsProposeAndRecord(t *testing.T) {
	store := &memStore{recent: ExtractLimit}
	prop := &scriptPropose{bodies: []string{`[{"claim":"Ada prefers tea","changes_behavior":true}]`}}
	emb := &scriptEmbed{}
	rep, err := Extract(context.Background(), store, prop, emb, testSource("Ada prefers tea every morning"))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Limited || prop.calls != 0 || emb.calls != 0 || store.records != 0 || len(store.added) != 0 {
		t.Fatalf("report=%+v calls=%d embed=%d records=%d added=%d", rep, prop.calls, emb.calls, store.records, len(store.added))
	}
}

func TestExtractBelowLimitProposes(t *testing.T) {
	store := &memStore{recent: ExtractLimit - 1}
	prop := &scriptPropose{bodies: []string{`[]`}}
	rep, err := Extract(context.Background(), store, prop, &scriptEmbed{}, testSource("Ada prefers tea every morning"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Limited || prop.calls != 1 || store.records != 1 {
		t.Fatalf("report=%+v calls=%d records=%d", rep, prop.calls, store.records)
	}
}

func TestExtractRecentUsesSourceKindIDAndWindow(t *testing.T) {
	store := &memStore{}
	prop := &scriptPropose{bodies: []string{`[]`}}
	src := testSource("Ada prefers tea every morning")
	if _, err := Extract(context.Background(), store, prop, &scriptEmbed{}, src); err != nil {
		t.Fatal(err)
	}
	if store.recentCalls != 1 || store.recentKind != src.Kind || store.recentID != src.ID {
		t.Fatalf("calls=%d kind=%q id=%q", store.recentCalls, store.recentKind, store.recentID)
	}
	if want := src.At.Add(-ExtractWindow); !store.recentSince.Equal(want) {
		t.Fatalf("since = %s, want %s", store.recentSince, want)
	}
}
