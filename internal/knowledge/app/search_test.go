package app

import (
	"context"
	"testing"
)

func TestSearchDenseWorksWhenFTSEmpty(t *testing.T) {
	finder := &searchFinder{
		near: []Lexical{{SourceID: sourceID, Seq: 0, Body: "paraphrase only"}},
	}
	embed := &scriptEmbed{model: "m", vector: hot(0)}
	got, err := Search(context.Background(), finder, embed, nil, allowAll, folderID, "tea")
	if err != nil {
		t.Fatal(err)
	}
	if embed.calls != 1 || finder.nearCalls != 1 {
		t.Fatalf("embed=%d near=%d", embed.calls, finder.nearCalls)
	}
	if len(got) != 1 || got[0].Body != "paraphrase only" {
		t.Fatalf("hits = %+v", got)
	}
}

func TestSearchEmptyWhenBothListsEmpty(t *testing.T) {
	finder := &searchFinder{}
	embed := &scriptEmbed{model: "m", vector: hot(0)}
	got, err := Search(context.Background(), finder, embed, nil, allowAll, folderID, "tea")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("hits=%d", len(got))
	}
}

func TestSearchFTSOrderWithoutEmbedder(t *testing.T) {
	finder := &searchFinder{lexical: []Lexical{
		{SourceID: sourceID, Seq: 0, Body: "first"},
		{SourceID: sourceID, Seq: 1, Body: "second"},
	}}
	got, err := Search(context.Background(), finder, nil, nil, allowAll, folderID, "tea")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Body != "first" || got[1].Body != "second" {
		t.Fatalf("hits = %+v", got)
	}
	if finder.nearCalls != 0 {
		t.Fatalf("near calls = %d", finder.nearCalls)
	}
}

func TestSearchDenseAddsParaphraseMissedByFTS(t *testing.T) {
	other := "018f3b2c-7a10-7c4a-8e11-dddddddddddd"
	finder := &searchFinder{
		lexical: []Lexical{{SourceID: sourceID, Seq: 0, Body: "exact token tea"}},
		near:    []Lexical{{SourceID: other, Seq: 0, Body: "a warm infusion she likes"}},
	}
	embed := &scriptEmbed{model: "m", vector: hot(0)}
	got, err := Search(context.Background(), finder, embed, nil, allowAll, folderID, "oolong brew")
	if err != nil {
		t.Fatal(err)
	}
	if finder.nearCalls != 1 {
		t.Fatalf("near calls = %d", finder.nearCalls)
	}
	found := false
	for _, hit := range got {
		if hit.SourceID == other && hit.Body == "a warm infusion she likes" {
			found = true
		}
	}
	if !found {
		t.Fatalf("dense-only hit missing: %+v", got)
	}
}

func TestSearchRerankerCannotInventIDs(t *testing.T) {
	finder := &searchFinder{lexical: []Lexical{
		{SourceID: sourceID, Seq: 0, Body: "first"},
		{SourceID: sourceID, Seq: 1, Body: "second"},
	}}
	got, err := Search(context.Background(), finder, nil, inventedReranker{}, allowAll, folderID, "tea")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Body != "first" || got[1].Body != "second" {
		t.Fatalf("hits = %+v", got)
	}
}

func TestSearchRejectsABlankQuery(t *testing.T) {
	finder := &searchFinder{}
	_, err := Search(context.Background(), finder, nil, nil, allowAll, folderID, "  ")
	if err == nil || finder.lexicalCalls != 0 {
		t.Fatalf("err=%v calls=%d", err, finder.lexicalCalls)
	}
}

func TestSearchPreservesCuratedAndWebProvenance(t *testing.T) {
	other := "018f3b2c-7a10-7c4a-8e11-dddddddddddd"
	finder := &searchFinder{
		lexical: []Lexical{
			{SourceID: sourceID, Seq: 0, Body: "curated guide", Provenance: ProvenanceCurated},
			{SourceID: other, Seq: 0, Body: "web clip", Provenance: ProvenanceWeb},
		},
	}
	got, err := Search(context.Background(), finder, nil, nil, allowAll, folderID, "guide")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("hits = %+v", got)
	}
	if got[0].Provenance != ProvenanceCurated || got[0].Provenance.Untrusted() {
		t.Fatalf("curated hit = %+v", got[0])
	}
	if got[1].Provenance != ProvenanceWeb || !got[1].Provenance.Untrusted() {
		t.Fatalf("web hit = %+v", got[1])
	}
}

func TestSearchTreatsEmptyProvenanceAsUntrusted(t *testing.T) {
	if !Provenance("").Untrusted() {
		t.Fatal("empty provenance must fail closed as untrusted")
	}
	finder := &searchFinder{
		lexical: []Lexical{{SourceID: sourceID, Seq: 0, Body: "unknown origin", Provenance: ""}},
	}
	got, err := Search(context.Background(), finder, nil, nil, allowAll, folderID, "origin")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Provenance.Untrusted() {
		t.Fatalf("expected empty provenance hit to stay untrusted, got %+v", got)
	}
}

type searchFinder struct {
	lexicalCalls int
	nearCalls    int
	lexical      []Lexical
	near         []Lexical
}

func (f *searchFinder) Lexical(context.Context, Access, string, string, int) ([]Lexical, error) {
	f.lexicalCalls++
	return f.lexical, nil
}

func (f *searchFinder) Near(context.Context, Access, string, []float32, int) ([]Lexical, error) {
	f.nearCalls++
	return f.near, nil
}

type inventedReranker struct{}

func (inventedReranker) Rerank(context.Context, string, []Hit) ([]Hit, error) {
	return []Hit{{SourceID: "nope", Seq: 9, Body: "invented"}}, nil
}
