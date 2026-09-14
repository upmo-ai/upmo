package app

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/chat/domain"
)

const userID = "018f3b2c-7a10-7c4a-8e11-111111111111"

func TestOpenNeutralisesTheProfile(t *testing.T) {
	book := &fakeBook{}
	access := Access{UserID: userID, Allow: "chat:create"}
	if _, err := Open(context.Background(), book, access, " See {{prior}} "); err != nil {
		t.Fatal(err)
	}
	if book.profile != "See { {prior}}" {
		t.Fatalf("profile = %q", book.profile)
	}
	if _, err := Open(context.Background(), book, Access{UserID: userID}, "Ada"); !errors.Is(err, ErrAccess) {
		t.Fatalf("empty allow error = %v", err)
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

func TestReadBuildsTheBlock(t *testing.T) {
	book := &fakeBook{profile: "Ada", lines: []string{"hello"}}
	access := Access{UserID: userID, Allow: "chat:view"}
	chatID := "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
	got, err := Read(context.Background(), book, access, chatID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Block != domain.Block("Ada") || len(got.Lines) != 1 {
		t.Fatalf("read = %+v", got)
	}
}

type fakeBook struct {
	opens   int
	says    int
	profile string
	body    string
	lines   []string
}

func (f *fakeBook) Open(_ context.Context, _ Access, profile string) (string, error) {
	f.opens++
	f.profile = profile
	return "018f3b2c-7a10-7c4a-8e11-cccccccccccc", nil
}

func (f *fakeBook) Say(_ context.Context, _ Access, _, body string) error {
	f.says++
	f.body = body
	return nil
}

func (f *fakeBook) Read(context.Context, Access, string) (Chat, error) {
	return Chat{Profile: f.profile, Lines: f.lines}, nil
}
