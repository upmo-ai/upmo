package app

import (
	"context"
	"testing"
)

func TestSearchMergeKeepsUntrustedProvenance(t *testing.T) {
	for _, tc := range []struct {
		name    string
		lexical Provenance
		dense   Provenance
	}{
		{name: "untrusted second", lexical: ProvenanceCurated, dense: ProvenanceWeb},
		{name: "untrusted first", lexical: ProvenanceWeb, dense: ProvenanceCurated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			finder := &searchFinder{
				lexical: []Lexical{{SourceID: sourceID, Seq: 0, Body: "same", Provenance: tc.lexical}},
				near:    []Lexical{{SourceID: sourceID, Seq: 0, Body: "same", Provenance: tc.dense}},
			}
			got, err := Search(context.Background(), finder, &scriptEmbed{model: "m", vector: hot(0)}, nil, allowAll, folderID, "tea")
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || !got[0].Provenance.Untrusted() {
				t.Fatalf("hits = %+v, want one untrusted hit", got)
			}
		})
	}
}
