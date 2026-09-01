package domain

import "testing"

func TestGate(t *testing.T) {
	activeClaim := "The account prefers weekly summaries"
	forgottenClaim := "The account uses compact notifications"

	tests := []struct {
		name       string
		input      Input
		wantAction Action
		wantReason string
		wantHash   bool
	}{
		{
			name: "general claim",
			input: Input{
				PreparedSource: "The account prefers weekly summaries",
				Candidate:      Candidate{Claim: "The account prefers weekly summaries"},
			},
			wantAction: ActionDrop,
			wantReason: "general",
		},
		{
			name: "imperative",
			input: Input{
				PreparedSource: "The account prefers weekly summaries",
				Candidate: Candidate{
					Claim:           "From now on the account prefers weekly summaries",
					ChangesBehavior: true,
				},
			},
			wantAction: ActionDrop,
			wantReason: "imperative",
		},
		{
			name: "bounded window even with date",
			input: Input{
				PreparedSource: "On 2026-10-31 the account prefers weekly summaries",
				Candidate: Candidate{
					Claim:           "Today, 2026-10-31, the account prefers weekly summaries",
					ChangesBehavior: true,
				},
			},
			wantAction: ActionDrop,
			wantReason: "bounded_window",
		},
		{
			name: "deny echo",
			input: Input{
				PreparedSource: "The project deployment region remains northern Europe",
				Candidate: Candidate{
					Claim:           "The project deployment region remains northern Europe",
					ChangesBehavior: true,
				},
				DenyClaims: []string{"The project deployment region remains northern Europe"},
			},
			wantAction: ActionDrop,
			wantReason: "echo",
		},
		{
			name: "strict superset is not echo",
			input: Input{
				PreparedSource: "The project deployment region remains northern Europe permanently",
				Candidate: Candidate{
					Claim:           "The project deployment region remains northern Europe permanently",
					ChangesBehavior: true,
				},
				DenyClaims: []string{"The project deployment region remains northern Europe"},
			},
			wantAction: ActionKeep,
			wantReason: "new",
			wantHash:   true,
		},
		{
			name: "ungrounded paraphrase",
			input: Input{
				PreparedSource: "Alice chooses coffee every morning",
				Candidate: Candidate{
					Claim:           "She enjoys espresso each sunrise",
					ChangesBehavior: true,
				},
			},
			wantAction: ActionDrop,
			wantReason: "ungrounded",
		},
		{
			name: "active hash reinforces",
			input: Input{
				PreparedSource: "The account prefers weekly summaries",
				Candidate: Candidate{
					Claim:           activeClaim,
					ChangesBehavior: true,
				},
				ActiveHashes: [][32]byte{ClaimHash(activeClaim)},
			},
			wantAction: ActionReinforce,
			wantReason: "active",
			wantHash:   true,
		},
		{
			name: "forgotten hash drops",
			input: Input{
				PreparedSource: "The account uses compact notifications",
				Candidate: Candidate{
					Claim:           forgottenClaim,
					ChangesBehavior: true,
				},
				ForgottenHashes: [][32]byte{ClaimHash(forgottenClaim)},
			},
			wantAction: ActionDrop,
			wantReason: "forgotten",
			wantHash:   true,
		},
		{
			name: "prefer is kept",
			input: Input{
				PreparedSource: "The account may prefer detailed notifications",
				Candidate: Candidate{
					Claim:           "The account may prefer detailed notifications",
					ChangesBehavior: true,
				},
			},
			wantAction: ActionKeep,
			wantReason: "new",
			wantHash:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Gate(tt.input)
			if got.Action != tt.wantAction {
				t.Errorf("Action = %q, want %q", got.Action, tt.wantAction)
			}
			if got.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tt.wantReason)
			}
			if tt.wantHash && got.Hash == ([32]byte{}) {
				t.Error("Hash is zero")
			}
			if !tt.wantHash && got.Hash != ([32]byte{}) {
				t.Error("Hash was computed before its gate")
			}
		})
	}
}

func TestGateChecksActiveBeforeForgotten(t *testing.T) {
	claim := "The account prefers weekly summaries"
	hash := ClaimHash(claim)
	got := Gate(Input{
		PreparedSource: "The account prefers weekly summaries",
		Candidate: Candidate{
			Claim:           claim,
			ChangesBehavior: true,
		},
		ActiveHashes:    [][32]byte{hash},
		ForgottenHashes: [][32]byte{hash},
	})

	if got.Action != ActionReinforce || got.Reason != "active" {
		t.Fatalf("Gate() = %#v, want active reinforcement", got)
	}
}
