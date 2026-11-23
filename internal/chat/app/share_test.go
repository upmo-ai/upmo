package app

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/chat/domain"
)

type fakeShareBook struct {
	row       ChatShare
	saveCalls int
}

func (f *fakeShareBook) ShareRow(context.Context, Access, string) (ChatShare, error) {
	return f.row, nil
}

func (f *fakeShareBook) SaveShare(context.Context, Access, string, string, string, []domain.ShareGrant, string) error {
	f.saveCalls++
	return nil
}

type fakeShareGrants struct {
	names map[string]string
}

func (f fakeShareGrants) ChatGrants(context.Context, string) ([]ShareMember, error) {
	return nil, nil
}

func (f fakeShareGrants) ReplaceChatGrants(context.Context, string, []domain.ShareGrant, string) error {
	return nil
}

func (f fakeShareGrants) Names(_ context.Context, persons, _ []string) (map[string]string, map[string]string, error) {
	out := make(map[string]string, len(persons))
	for _, id := range persons {
		if name, ok := f.names[id]; ok {
			out[id] = name
		}
	}
	return out, map[string]string{}, nil
}

func (f fakeShareGrants) TeamsOf(context.Context, string) ([]string, error) {
	return nil, nil
}

func TestWriteShareRefusesSharedEditorBeforeCallingSaveShare(t *testing.T) {
	const (
		ownerID  = "018f3b2c-7a10-7c4a-8e11-111111111111"
		editorID = "018f3b2c-7a10-7c4a-8e11-222222222222"
		thirdID  = "018f3b2c-7a10-7c4a-8e11-333333333333"
		chatID   = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa"
	)

	book := &fakeShareBook{
		row: ChatShare{
			OwnerID:    ownerID,
			Visibility: domain.VisibilityPrivate,
			Editable:   true,
		},
	}
	grants := fakeShareGrants{
		names: map[string]string{
			ownerID:  "Ada",
			editorID: "Bob",
			thirdID:  "Cy",
		},
	}
	editorAccess := Access{
		UserID:     editorID,
		Allow:      "chat:view,chat:edit",
		SharedView: "{" + chatID + "}",
		SharedEdit: "{" + chatID + "}",
	}
	update := domain.ShareUpdate{
		Visibility: domain.VisibilityPrivate,
		Grants: []domain.ShareGrant{
			{SubjectType: domain.SharePerson, SubjectID: ownerID, Role: domain.ShareOwner},
			{SubjectType: domain.SharePerson, SubjectID: editorID, Role: domain.ShareEditor},
			{SubjectType: domain.SharePerson, SubjectID: thirdID, Role: domain.ShareEditor},
		},
	}

	_, err := WriteShare(context.Background(), book, grants, editorAccess, chatID, update)
	if !errors.Is(err, ErrAccess) {
		t.Fatalf("WriteShare as shared editor = %v, want ErrAccess", err)
	}
	if book.saveCalls != 0 {
		t.Fatalf("SaveShare called %d times, want 0", book.saveCalls)
	}
}
