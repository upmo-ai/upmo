package webauthn

import (
	"testing"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

func TestSoftCeremony(t *testing.T) {
	ceremony, err := New("localhost", "http://localhost:5173")
	if err != nil {
		t.Fatal(err)
	}
	account := domain.Account{Handle: []byte("handle-handle-handle-handle-hand"), Name: "ada@example.com", Display: "Ada"}
	options, session, err := ceremony.BeginRegistration(account)
	if err != nil || len(options) == 0 {
		t.Fatal(err)
	}
	challenge, err := Challenge(session)
	if err != nil {
		t.Fatal(err)
	}
	soft, err := NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	body, err := soft.Register("localhost", "http://localhost:5173", challenge, account.Handle)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := ceremony.FinishRegistration(account, session, body)
	if err != nil {
		t.Fatal(err)
	}
	account.Credentials = []domain.Credential{credential}

	_, loginSession, err := ceremony.BeginDiscoverable()
	if err != nil {
		t.Fatal(err)
	}
	loginChallenge, err := Challenge(loginSession)
	if err != nil {
		t.Fatal(err)
	}
	assertion, err := soft.Login("localhost", "http://localhost:5173", loginChallenge, account.Handle)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := ceremony.FinishDiscoverable(loginSession, assertion, func(_, _ []byte) (domain.Account, error) {
		return account, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != account.Name {
		t.Fatalf("account = %s", got.Name)
	}
	bad, err := soft.Login("localhost", "https://evil.example", loginChallenge, account.Handle)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ceremony.FinishDiscoverable(loginSession, bad, func(_, _ []byte) (domain.Account, error) {
		return account, nil
	}); err == nil {
		t.Fatal("another origin was accepted")
	}
}
