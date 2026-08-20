package gateway

import "testing"

func TestEnvSecrets(t *testing.T) {
	t.Setenv("UPMO_CREDENTIAL_house", "s")
	s := EnvSecrets{}
	got, err := s.Get("house")
	if err != nil || got != "s" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := s.Get("missing"); err == nil {
		t.Fatal("missing")
	}
	if _, err := s.Get("bad-ref"); err == nil {
		t.Fatal("bad ref")
	}
}
