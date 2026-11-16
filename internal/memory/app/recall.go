package app

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/upmo-ai/upmo/internal/platform/trustfence"
	"github.com/upmo-ai/upmo/internal/platform/writeguard"
)

// ProfileTokens is the profile budget in the cacheable prefix.
const ProfileTokens = 300

// SearchLimit is the most claims the word-overlap Search returns.
const SearchLimit = 5

// Memory is the profile and the active claims for one person.
type Memory struct {
	Profile string
	Claims  []string
}

// Reader loads one person's profile and active claims.
type Reader interface {
	Load(ctx context.Context, userID string) (Memory, error)
}

// SystemPrefix places the profile after the standing instructions. The
// caller passes the result as the chat system text, which is the cacheable
// prefix. Extraction does not write this text.
func SystemPrefix(instructions, profile string) string {
	profile = trustfence.NeutralizeSlot(trimProfile(profile))
	if profile == "" {
		return instructions
	}
	block := "Profile:\n" + profile
	if strings.TrimSpace(instructions) == "" {
		return block
	}
	return instructions + "\n\n" + block
}

// Recall is one search_memory result. Calling search sets Private even when
// nothing matches. User/admin-confirmed memory facts are fenced for prompt
// hygiene but do not mark Untrusted.
type Recall struct {
	Text     string
	Exposure writeguard.Exposure
}

// Search ranks active claims against the query and fences the hits.
func Search(query string, claims []string) Recall {
	return fenceClaims(rankClaims(query, claims))
}

// fenceClaims fences each claim on its own line, in the order given, and
// sets Private (with Untrusted false) even when there are no claims.
func fenceClaims(claims []string) Recall {
	recall := Recall{Exposure: writeguard.Exposure{Untrusted: false, Private: true}}
	if len(claims) == 0 {
		return recall
	}
	var b strings.Builder
	for i, claim := range claims {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(trustfence.Wrap(trustfence.TagMemory, "fact", trustfence.NeutralizeSlot(claim)))
	}
	recall.Text = b.String()
	return recall
}

func trimProfile(profile string) string {
	profile = strings.TrimSpace(profile)
	maxBytes := ProfileTokens * 4
	if len(profile) <= maxBytes {
		return profile
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(profile[cut]) {
		cut--
	}
	for i := cut; i >= cut-80 && i > 0; i-- {
		if profile[i] == ' ' || profile[i] == '\n' {
			cut = i
			break
		}
	}
	return strings.TrimSpace(profile[:cut])
}

func rankClaims(query string, claims []string) []string {
	needles := words(query)
	if len(needles) == 0 {
		return nil
	}
	type hit struct {
		claim string
		score int
	}
	var hits []hit
	for _, claim := range claims {
		score := 0
		for word := range words(claim) {
			if _, ok := needles[word]; ok {
				score++
			}
		}
		if score == 0 {
			continue
		}
		hits = append(hits, hit{claim: claim, score: score})
	}
	for i := 1; i < len(hits); i++ {
		j := i
		for j > 0 && hits[j].score > hits[j-1].score {
			hits[j], hits[j-1] = hits[j-1], hits[j]
			j--
		}
	}
	if len(hits) > SearchLimit {
		hits = hits[:SearchLimit]
	}
	out := make([]string, len(hits))
	for i, hit := range hits {
		out[i] = hit.claim
	}
	return out
}

func words(s string) map[string]struct{} {
	out := map[string]struct{}{}
	var b strings.Builder
	add := func() {
		if b.Len() == 0 {
			return
		}
		word := strings.ToLower(b.String())
		b.Reset()
		if utf8.RuneCountInString(word) < 4 {
			return
		}
		out[word] = struct{}{}
	}
	for _, r := range s {
		if unicode.IsLetter(r) {
			b.WriteRune(r)
			continue
		}
		add()
	}
	add()
	return out
}
