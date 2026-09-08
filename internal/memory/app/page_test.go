package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/memory/domain"
)

func TestSaveProfileRejectsThePrefixBudget(t *testing.T) {
	ed := &pageEditor{}
	body := strings.Repeat("a", ProfileTokens*4+1)
	err := SaveProfile(context.Background(), ed, testUser, body, time.Now())
	if !errors.Is(err, ErrProfile) || ed.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, ed.calls)
	}
}

func TestSaveProfileRedactsAnEmail(t *testing.T) {
	ed := &pageEditor{person: &pagePerson{}}
	err := SaveProfile(context.Background(), ed, testUser, " Reach me at ada@example.com. ", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if ed.person.profile != "Reach me at ***." {
		t.Fatalf("profile=%q", ed.person.profile)
	}
}

func TestAddFactRejectsEmpty(t *testing.T) {
	ed := &pageEditor{}
	_, err := AddFact(context.Background(), ed, testUser, "   ", time.Now())
	if !errors.Is(err, ErrClaim) || ed.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, ed.calls)
	}
}

func TestAddFactStoresTheNormalisedHash(t *testing.T) {
	ed := &pageEditor{person: &pagePerson{id: "018f3b2c-7a10-7c4a-8e11-222222222222"}}
	id, err := AddFact(context.Background(), ed, testUser, "  Ada prefers tea. ", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if id != ed.person.id || ed.person.claim != "Ada prefers tea." {
		t.Fatalf("id=%s claim=%q", id, ed.person.claim)
	}
	if ed.person.hash != domainHash("Ada prefers tea.") {
		t.Fatal("hash")
	}
}

func TestAddFactStoresTheClaimWithoutTheEmail(t *testing.T) {
	ed := &pageEditor{person: &pagePerson{id: "018f3b2c-7a10-7c4a-8e11-222222222222"}}
	if _, err := AddFact(context.Background(), ed, testUser, "Email ada@example.com please", time.Now()); err != nil {
		t.Fatal(err)
	}
	if ed.person.claim != "Email *** please" {
		t.Fatalf("claim=%q", ed.person.claim)
	}
	if ed.person.hash != domain.ClaimHash(ed.person.claim) {
		t.Fatal("hash is not of the stored claim")
	}
}

func TestEditFactKeepsTheSameRowWhenTheWordingIsUnchanged(t *testing.T) {
	ed := &pageEditor{person: &pagePerson{id: "018f3b2c-7a10-7c4a-8e11-222222222222", same: true}}
	next, err := EditFact(context.Background(), ed, testUser, ed.person.id, "Ada prefers tea", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if next != ed.person.id {
		t.Fatalf("next=%s", next)
	}
}

type pageEditor struct {
	person *pagePerson
	calls  int
}

func (e *pageEditor) InPerson(_ context.Context, userID string, fn func(Person) error) error {
	e.calls++
	if userID != testUser {
		return errors.New("user")
	}
	if e.person == nil {
		e.person = &pagePerson{}
	}
	return fn(e.person)
}

type pagePerson struct {
	id      string
	claim   string
	hash    [32]byte
	same    bool
	profile string
}

func (p *pagePerson) WriteProfile(_ context.Context, body string, _ time.Time) error {
	p.profile = body
	return nil
}

func (p *pagePerson) ReadPage(context.Context) (Page, error)             { return Page{}, nil }
func (p *pagePerson) Tombstone(context.Context, string, time.Time) error { return nil }

func (p *pagePerson) InsertUser(_ context.Context, claim string, hash [32]byte, _ time.Time) (string, error) {
	p.claim = claim
	p.hash = hash
	return p.id, nil
}

func (p *pagePerson) Supersede(_ context.Context, id, claim string, hash [32]byte, _ time.Time) (string, error) {
	p.claim = claim
	p.hash = hash
	if p.same {
		return id, nil
	}
	return "018f3b2c-7a10-7c4a-8e11-555555555555", nil
}

func domainHash(claim string) [32]byte {
	_, hash, err := preparedClaim(testUser, claim)
	if err != nil {
		panic(err)
	}
	return hash
}
