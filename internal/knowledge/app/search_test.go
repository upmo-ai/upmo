package app

import (
	"context"
	"testing"
)

func TestSearchDoesNotEmbedWhenNothingMatches(t *testing.T) {
	finder := &lexicalFinder{}
	embed := &scriptEmbed{model: "m", vector: hot(0)}
	got, err := Search(context.Background(), finder, embed, nil, allowAll, folderID, "tea")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || embed.calls != 0 {
		t.Fatalf("hits=%d embed=%d", len(got), embed.calls)
	}
}

func TestSearchReordersLexicalHitsAndDoesNotAddAny(t *testing.T) {
	finder := &lexicalFinder{hits: []Lexical{
		{SourceID: sourceID, Seq: 0, Body: "first", Vector: hot(0)},
		{SourceID: sourceID, Seq: 1, Body: "second", Vector: hot(1)},
	}}
	embed := &scriptEmbed{model: "m", vector: hot(1)}
	got, err := Search(context.Background(), finder, embed, nil, allowAll, folderID, "tea")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Body != "second" || got[1].Body != "first" {
		t.Fatalf("hits = %+v", got)
	}
}

func TestSearchKeepsLexicalOrderWhenTheRerankerInventsIDs(t *testing.T) {
	finder := &lexicalFinder{hits: []Lexical{
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
	finder := &lexicalFinder{}
	_, err := Search(context.Background(), finder, nil, nil, allowAll, folderID, "  ")
	if err == nil || finder.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, finder.calls)
	}
}

type lexicalFinder struct {
	calls int
	hits  []Lexical
}

func (f *lexicalFinder) Lexical(context.Context, Access, string, string, int) ([]Lexical, error) {
	f.calls++
	return f.hits, nil
}

type inventedReranker struct{}

func (inventedReranker) Rerank(context.Context, string, []Hit) ([]Hit, error) {
	return []Hit{{SourceID: "nope", Seq: 9, Body: "invented"}}, nil
}
