package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestProposeAtTheCapReturnsEmpty(t *testing.T) {
	got, err := Propose(DailyCap, []string{"File the receipts"})
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("at cap = %#v %v", got, err)
	}
}

func TestProposeKeepsOnlyTheRoomLeft(t *testing.T) {
	got, err := Propose(4, []string{"  File the receipts  ", "Chase the invoice"})
	if err != nil || len(got) != 1 {
		t.Fatalf("room of one = %#v %v", got, err)
	}
	if got[0].Kind != NoticeProposal || got[0].Title != "File the receipts" {
		t.Fatalf("notice = %#v", got[0])
	}
}

func TestProposeDropsABlank(t *testing.T) {
	got, err := Propose(0, []string{"   ", "File the receipts"})
	if err != nil || len(got) != 1 || got[0].Title != "File the receipts" {
		t.Fatalf("blank = %#v %v", got, err)
	}
}

func TestProposeDropsARepeat(t *testing.T) {
	got, err := Propose(0, []string{"File the receipts", " File the receipts ", "Chase the invoice"})
	if err != nil || len(got) != 2 {
		t.Fatalf("repeat = %#v %v", got, err)
	}
	if got[0].Title != "File the receipts" || got[1].Title != "Chase the invoice" {
		t.Fatalf("titles = %#v", got)
	}
}

func TestProposeRefusesAnOverLongTitle(t *testing.T) {
	long := strings.Repeat("é", ProposalRunes+1)
	got, err := Propose(0, []string{"File the receipts", long})
	if !errors.Is(err, ErrScout) || got != nil {
		t.Fatalf("over long = %#v %v", got, err)
	}
}

func TestProposeRefusesACredential(t *testing.T) {
	got, err := Propose(0, []string{"File the receipts", "Rotate SK-live key"})
	if !errors.Is(err, ErrScout) || got != nil {
		t.Fatalf("credential = %#v %v", got, err)
	}
}

func TestProposeRefusesANegativeCount(t *testing.T) {
	got, err := Propose(-1, []string{"File the receipts"})
	if !errors.Is(err, ErrScout) || got != nil {
		t.Fatalf("negative = %#v %v", got, err)
	}
}
