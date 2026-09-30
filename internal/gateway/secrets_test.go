package gateway

import (
	"strings"
	"testing"
)

func TestEnvSecrets(t *testing.T) {
	t.Setenv("UPMO_CREDENTIAL_house", "s")
	s := EnvSecrets{}
	got, err := s.Get("house")
	if err != nil || got != "s" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := s.Get("absent"); err == nil || strings.Contains(err.Error(), "absent") {
		t.Fatalf("missing err = %v", err)
	}
	if _, err := s.Get("sk-live"); err == nil || strings.Contains(err.Error(), "sk-live") {
		t.Fatalf("bad ref err = %v", err)
	}
}
