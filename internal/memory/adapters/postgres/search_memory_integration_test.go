//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/memory/app"
)

func TestSearchMemoryRetrievesCurrentPrivateFacts(t *testing.T) {
	store, asOf := retrieveStore(t)
	got, err := app.SearchMemory(
		context.Background(), store, retrieveEmbedder{index: 0}, nil,
		person, "oolong", asOf,
	)
	if err != nil {
		t.Fatal(err)
	}

	ada := strings.Index(got.Text, "Ada prefers oolong tea")
	if ada < 0 {
		t.Fatalf("missing current fact: %q", got.Text)
	}
	if lee := strings.Index(got.Text, "Lee drinks coffee"); lee >= 0 && lee < ada {
		t.Fatalf("coffee preceded oolong: %q", got.Text)
	}
	if strings.Contains(got.Text, "previously preferred") {
		t.Fatalf("forgotten fact returned: %q", got.Text)
	}
	if strings.Contains(got.Text, "Other person") {
		t.Fatalf("another person's fact returned: %q", got.Text)
	}

	const opener = `<memory source="fact">`
	const closer = `</memory>`
	fences := strings.Count(got.Text, opener)
	if fences == 0 {
		t.Fatalf("missing memory fence: %q", got.Text)
	}
	if closers := strings.Count(got.Text, closer); closers != fences {
		t.Fatalf("raw memory breaker returned: %q", got.Text)
	}

	if got.Exposure.Untrusted || !got.Exposure.Private || got.Exposure.External {
		t.Fatalf("exposure = %+v", got.Exposure)
	}
	if prefix := app.SystemPrefix("Be brief.", "Ada lives in Dhaka"); strings.Contains(prefix, "oolong") {
		t.Fatalf("search result leaked into system prefix: %q", prefix)
	}
}
