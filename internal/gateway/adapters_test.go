package gateway

import (
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

func TestAnthropicIsAKnownAdapterWithNoEmbeddings(t *testing.T) {
	table := adapters()
	native, ok := table[domain.AdapterAnthropic]
	if !ok {
		t.Fatal("the anthropic adapter is not in the table")
	}
	p := NewProviders(nil, Keyring{}, nil)
	if !p.Known(domain.AdapterAnthropic) || p.Probes[domain.AdapterAnthropic] == nil {
		t.Error("the provider settings do not know the anthropic adapter")
	}
	// Mount lists an embed adapter only for an adapter that can embed, so an
	// embed route that reaches anthropic finds none and moves on.
	if _, embeds := native.(app.Embedder); embeds {
		t.Error("the anthropic adapter is offered for embeddings")
	}
	for _, key := range []string{domain.AdapterOpenAI, domain.AdapterFake} {
		if _, embeds := table[key].(app.Embedder); !embeds {
			t.Errorf("adapter %q lost its embeddings", key)
		}
	}
}
