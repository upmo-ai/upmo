package app

import (
	"context"
	"errors"
	"strings"
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
	if err := Say(context.Background(), book, access, chatID, " {{no}} ", Picks{}); err != nil {
		t.Fatal(err)
	}
	if book.body != "{ {no}}" {
		t.Fatalf("body = %q", book.body)
	}
	if err := Say(context.Background(), book, access, "bad", "hello", Picks{}); !errors.Is(err, ErrChat) {
		t.Fatalf("bad id error = %v", err)
	}
	if book.says != 1 {
		t.Fatalf("says = %d", book.says)
	}
}

func TestSayChecksTheRoute(t *testing.T) {
	book := &fakeBook{}
	access := Access{UserID: userID, Allow: "chat:edit"}
	chatID := "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
	if err := Say(context.Background(), book, access, chatID, "hello", Picks{Route: " draft "}); err != nil || book.route != "draft" {
		t.Fatalf("route %q error %v", book.route, err)
	}
	if err := Say(context.Background(), book, access, chatID, "hello", Picks{Route: "has space"}); !errors.Is(err, domain.ErrRoute) {
		t.Fatalf("bad route error = %v", err)
	}
	if book.says != 1 {
		t.Fatalf("says = %d", book.says)
	}
}

func TestRetryChecksItsInput(t *testing.T) {
	book := &fakeBook{}
	access := Access{UserID: userID, Allow: "chat:edit"}
	chatID := "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
	if err := Retry(context.Background(), book, access, "bad", chatID, nil); !errors.Is(err, ErrChat) {
		t.Fatalf("bad chat error = %v", err)
	}
	if err := Retry(context.Background(), book, access, chatID, "bad", nil); !errors.Is(err, ErrNoLine) {
		t.Fatalf("bad line error = %v", err)
	}
	if err := Retry(context.Background(), book, Access{}, chatID, chatID, nil); !errors.Is(err, ErrAccess) {
		t.Fatalf("no access error = %v", err)
	}
}

const pickAccount = "018f3b2c-7a10-7c4a-8e11-dddddddddddd"

func TestSayChecksThePinAndTheAgent(t *testing.T) {
	book := &fakeBook{}
	access := Access{UserID: userID, Allow: "chat:edit"}
	chatID := "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
	good := Picks{Route: "fast", PinAccount: strings.ToUpper(pickAccount), PinModel: " gpt-x ", AgentID: userID}
	if err := Say(context.Background(), book, access, chatID, "hello", good); err != nil {
		t.Fatal(err)
	}
	if want := (Picks{Route: "fast", PinAccount: pickAccount, PinModel: "gpt-x", AgentID: userID}); book.picks != want {
		t.Fatalf("picks stored = %+v, want %+v", book.picks, want)
	}
	for name, bad := range map[string]Picks{
		"account without a model":    {PinAccount: pickAccount},
		"model without an account":   {PinModel: "gpt-x"},
		"account that is not a uuid": {PinAccount: "mine", PinModel: "gpt-x"},
		"model too long":             {PinAccount: pickAccount, PinModel: strings.Repeat("m", domain.PinModelRunes+1)},
	} {
		if err := Say(context.Background(), book, access, chatID, "hello", bad); !errors.Is(err, domain.ErrPin) {
			t.Errorf("%s: error = %v", name, err)
		}
	}
	if err := Say(context.Background(), book, access, chatID, "hello", Picks{AgentID: "research"}); !errors.Is(err, domain.ErrAgent) {
		t.Errorf("agent that is not a uuid: error = %v", err)
	}
	if book.says != 1 {
		t.Fatalf("says = %d, a refused pick must not reach the book", book.says)
	}
}

func TestRetryPassesTheCleanedPicksOrNone(t *testing.T) {
	book := &fakeBook{}
	access := Access{UserID: userID, Allow: "chat:edit"}
	chatID := "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
	if err := Retry(context.Background(), book, access, chatID, chatID, nil); err != nil || book.retried != nil {
		t.Fatalf("retry with nothing sent %+v, error %v", book.retried, err)
	}
	given := Picks{Route: "fast", PinAccount: pickAccount, PinModel: "gpt-x"}
	if err := Retry(context.Background(), book, access, chatID, chatID, &given); err != nil || book.retried == nil || *book.retried != given {
		t.Fatalf("retry sent %+v, error %v", book.retried, err)
	}
	if err := Retry(context.Background(), book, access, chatID, chatID, &Picks{}); err != nil || book.retried == nil || !book.retried.None() {
		t.Fatalf("explicit empty picks reached the book as %+v, error %v", book.retried, err)
	}
	if err := Retry(context.Background(), book, access, chatID, chatID, &Picks{PinModel: "gpt-x"}); !errors.Is(err, domain.ErrPin) {
		t.Fatalf("half pin error = %v", err)
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
	route     string
	picks     Picks
	retried   *Picks
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

func (f *fakeBook) Say(_ context.Context, _ Access, _, body string, picks Picks) error {
	f.says++
	f.body = body
	f.route = picks.Route
	f.picks = picks
	return nil
}

func (f *fakeBook) Retry(_ context.Context, _ Access, _, _ string, picks *Picks) error {
	f.retried = picks
	return nil
}

func (f *fakeBook) Read(context.Context, Access, string) (Chat, error) {
	f.reads++
	return Chat{Lines: f.lines}, nil
}
