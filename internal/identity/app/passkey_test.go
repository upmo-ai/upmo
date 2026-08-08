package app

import "testing"

func TestLinkURLKeepsTheTokenInTheFragment(t *testing.T) {
	got := (Passkeys{PublicURL: "https://upmo.example"}).LinkURL("abc")
	if got != "https://upmo.example/login/link#abc" {
		t.Fatalf("url %s", got)
	}
}
