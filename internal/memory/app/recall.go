package app

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/upmo-ai/upmo/internal/platform/trustfence"
	"github.com/upmo-ai/upmo/internal/platform/writeguard"
)

// ProfileTokens is the profile budget in the cacheable prefix.
const ProfileTokens = 300

// Memory is the profile and the active claims for one person.
type Memory struct {
	Profile string
	Claims  []string
}

// Reader loads one person's profile and active claims.
type Reader interface {
	Load(ctx context.Context, userID string) (Memory, error)
}

// ProfileReader reads one person's profile body under row security.
type ProfileReader interface {
	Profile(ctx context.Context, userID string) (string, error)
}

// Profile reads one person's profile body.
func Profile(ctx context.Context, reader ProfileReader, userID string) (string, error) {
	return reader.Profile(ctx, userID)
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
