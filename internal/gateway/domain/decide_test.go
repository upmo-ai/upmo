package domain

import "testing"

func TestDecideRequestCheck(t *testing.T) {
	if err := (DecideRequest{Kind: DecisionYesNo, Prompt: "keep?"}).Check(); err != nil {
		t.Fatal(err)
	}
	if err := (DecideRequest{Kind: DecisionChoice, Prompt: "which", Options: []string{"a", "b"}}).Check(); err != nil {
		t.Fatal(err)
	}
	if err := (DecideRequest{Kind: DecisionYesNo}).Check(); err == nil {
		t.Fatal("empty prompt")
	}
	if err := (DecideRequest{Kind: DecisionYesNo, Prompt: "x", Options: []string{"a"}}).Check(); err == nil {
		t.Fatal("yesno with options")
	}
	if err := (DecideRequest{Kind: DecisionChoice, Prompt: "x", Options: []string{"a"}}).Check(); err == nil {
		t.Fatal("one option")
	}
	if err := (DecideRequest{Kind: DecisionChoice, Prompt: "x", Options: []string{"a", "a"}}).Check(); err == nil {
		t.Fatal("repeated option")
	}
	if err := (DecideRequest{Kind: DecisionNone, Prompt: "x"}).Check(); err == nil {
		t.Fatal("none")
	}
}

func TestParseDecideAnswers(t *testing.T) {
	yes, err := ParseYesNo("yes")
	if err != nil || !yes {
		t.Fatalf("yes: %v %v", yes, err)
	}
	no, err := ParseYesNo("no")
	if err != nil || no {
		t.Fatalf("no: %v %v", no, err)
	}
	if _, err := ParseYesNo("YES"); err == nil {
		t.Fatal("mixed case")
	}
	got, err := ParseChoice("b", []string{"a", "b"})
	if err != nil || got != "b" {
		t.Fatalf("choice: %q %v", got, err)
	}
	if _, err := ParseChoice("c", []string{"a", "b"}); err == nil {
		t.Fatal("missing option")
	}
}
