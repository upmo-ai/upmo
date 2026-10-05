package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestProposeDropsABlank(t *testing.T) {
	got, err := Propose([]string{"   ", "File the receipts"}, nil)
	if err != nil || len(got) != 1 || got[0].Title != "File the receipts" {
		t.Fatalf("blank = %#v %v", got, err)
	}
}

func TestProposeDropsARepeat(t *testing.T) {
	got, err := Propose([]string{"File the receipts", " File the receipts ", "Chase the invoice"}, nil)
	if err != nil || len(got) != 2 {
		t.Fatalf("repeat = %#v %v", got, err)
	}
	if got[0].Title != "File the receipts" || got[1].Title != "Chase the invoice" {
		t.Fatalf("titles = %#v", got)
	}
}

func TestProposeDropsAnOpenGoal(t *testing.T) {
	got, err := Propose(
		[]string{"File the receipts", "  file the receipts  ", "Chase the invoice"},
		[]string{"File the receipts"},
	)
	if err != nil || len(got) != 1 || got[0].Title != "Chase the invoice" {
		t.Fatalf("open goal = %#v %v", got, err)
	}
}

func TestProposeDropsAnOpenGoalWithDifferentCase(t *testing.T) {
	got, err := Propose([]string{"FILE THE RECEIPTS"}, []string{"  file the receipts "})
	if err != nil || len(got) != 0 {
		t.Fatalf("case = %#v %v", got, err)
	}
}

func TestProposeRefusesAnOverLongTitle(t *testing.T) {
	long := strings.Repeat("é", ProposalRunes+1)
	got, err := Propose([]string{"File the receipts", long}, nil)
	if !errors.Is(err, ErrScout) || got != nil {
		t.Fatalf("over long = %#v %v", got, err)
	}
}

func TestProposeRefusesACredential(t *testing.T) {
	got, err := Propose([]string{"File the receipts", "Rotate SK-live key"}, nil)
	if !errors.Is(err, ErrScout) || got != nil {
		t.Fatalf("credential = %#v %v", got, err)
	}
}

func TestProposeKindIsProposal(t *testing.T) {
	got, err := Propose([]string{"File the receipts"}, nil)
	if err != nil || len(got) != 1 || got[0].Kind != NoticeProposal {
		t.Fatalf("kind = %#v %v", got, err)
	}
}
