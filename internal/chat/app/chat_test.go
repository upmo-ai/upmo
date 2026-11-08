package app

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/chat/domain"
)

const userID = "018f3b2c-7a10-7c4a-8e11-111111111111"

func TestOpenRejectsABadCaller(t *testing.T) {
	book := &fakeBook{}
	access := Access{UserID: userID, Allow: "chat:create"}
	if _, err := Open(context.Background(), book, access); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), book, Access{UserID: userID}); !errors.Is(err, ErrAccess) {
		t.Fatalf("empty allow error = %v", err)
	}
	// A person who may only view or edit chats cannot open one: a 403, not a database error.
	if _, err := Open(context.Background(), book, Access{UserID: userID, Allow: "chat:view,chat:edit"}); !errors.Is(err, ErrAccess) {
		t.Fatalf("no chat:create error = %v", err)
	}
	if book.opens != 1 {
		t.Fatalf("opens = %d", book.opens)
	}
}

func TestSayNeutralisesTheLine(t *testing.T) {
	book := &fakeBook{}
	access := Access{UserID: userID, Allow: "chat:edit"}
	chatID := "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
	if err := Say(context.Background(), book, access, chatID, " {{no}} "); err != nil {
		t.Fatal(err)
	}
	if book.body != "{ {no}}" {
		t.Fatalf("body = %q", book.body)
	}
	if err := Say(context.Background(), book, access, "bad", "hello"); !errors.Is(err, ErrChat) {
		t.Fatalf("bad id error = %v", err)
	}
	if book.says != 1 {
		t.Fatalf("says = %d", book.says)
	}
}

func TestReadReturnsTheLines(t *testing.T) {
	book := &fakeBook{lines: []Line{{ID: "l1", Role: "user", Body: "hello"}}}
	access := Access{UserID: userID, Allow: "chat:view"}
	chatID := "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
	got, err := Read(context.Background(), book, access, chatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 1 || got.Lines[0].Body != "hello" {
		t.Fatalf("read = %+v", got)
	}
}

func TestListReturnsTheSummaries(t *testing.T) {
	book := &fakeBook{summaries: []ChatSummary{{ID: "018f3b2c-7a10-7c4a-8e11-cccccccccccc", Title: domain.NewChatTitle}}}
	access := Access{UserID: userID, Allow: "chat:view"}
	got, err := List(context.Background(), book, access)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "018f3b2c-7a10-7c4a-8e11-cccccccccccc" || got[0].Title != domain.NewChatTitle {
		t.Fatalf("list = %+v", got)
	}
	if book.lists != 1 {
		t.Fatalf("lists = %d", book.lists)
	}
}

func TestListReturnsAnEmptyListNotNil(t *testing.T) {
	book := &fakeBook{}
	access := Access{UserID: userID, Allow: "chat:view"}
	got, err := List(context.Background(), book, access)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("list = %#v", got)
	}
}

type fakeBook struct {
	opens     int
	says      int
	lists     int
	reads     int
	body      string
	lines     []Line
	summaries []ChatSummary
}

func (f *fakeBook) Open(context.Context, Access) (ChatSummary, error) {
	f.opens++
	return ChatSummary{ID: "018f3b2c-7a10-7c4a-8e11-cccccccccccc", Title: domain.NewChatTitle}, nil
}

func (f *fakeBook) List(context.Context, Access) ([]ChatSummary, error) {
	f.lists++
	return f.summaries, nil
}

func (f *fakeBook) Say(_ context.Context, _ Access, _, body string) error {
	f.says++
	f.body = body
	return nil
}

func (f *fakeBook) Read(context.Context, Access, string) (Chat, error) {
	f.reads++
	return Chat{Lines: f.lines}, nil
}
