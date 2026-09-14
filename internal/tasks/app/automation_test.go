package app

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

func TestSaveAutomationRejectsAnUnknownKind(t *testing.T) {
	book := &fakeAutomations{}
	access := Access{UserID: userID, Allow: "task:create"}
	templateID := "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
	if _, err := SaveAutomation(context.Background(), book, access, templateID, "manual", "daily"); !errors.Is(err, domain.ErrInput) {
		t.Fatalf("kind error = %v", err)
	}
	if book.saves != 0 {
		t.Fatalf("saves = %d", book.saves)
	}
}

func TestFireNeutralisesSlotValues(t *testing.T) {
	book := &fakeAutomations{}
	access := Access{UserID: userID, Allow: "task:create"}
	automationID := "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
	_, _, err := Fire(context.Background(), book, access, automationID, " monday ", map[string]string{"kind": " {{no}} "}, "")
	if err != nil {
		t.Fatal(err)
	}
	if book.dedup != "monday" || book.values["kind"] != "{ {no}}" {
		t.Fatalf("dedup = %q values = %#v", book.dedup, book.values)
	}
	if _, _, err := Fire(context.Background(), book, access, automationID, "  ", nil, ""); !errors.Is(err, domain.ErrInput) {
		t.Fatalf("blank key error = %v", err)
	}
	if book.fires != 1 {
		t.Fatalf("fires = %d", book.fires)
	}
}

func TestReassignRequiresAnAdmin(t *testing.T) {
	book := &fakeAutomations{}
	access := Access{UserID: userID, Allow: "task:edit"}
	automationID := "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
	other := "018f3b2c-7a10-7c4a-8e11-222222222222"
	if err := Reassign(context.Background(), book, access, automationID, other); !errors.Is(err, ErrAccess) {
		t.Fatalf("reassign error = %v", err)
	}
	if book.reassigns != 0 {
		t.Fatalf("reassigns = %d", book.reassigns)
	}
}

type fakeAutomations struct {
	saves     int
	fires     int
	reassigns int
	dedup     string
	values    map[string]string
}

func (f *fakeAutomations) SaveAutomation(context.Context, Access, string, string, string) (string, error) {
	f.saves++
	return "", nil
}

func (f *fakeAutomations) Fire(_ context.Context, _ Access, _, dedup string, values map[string]string, _ string) (string, bool, error) {
	f.fires++
	f.dedup = dedup
	f.values = values
	return "018f3b2c-7a10-7c4a-8e11-cccccccccccc", false, nil
}

func (f *fakeAutomations) SetMode(context.Context, Access, string, string) error { return nil }

func (f *fakeAutomations) OwnerLeft(context.Context, Access, string) error { return nil }

func (f *fakeAutomations) Reassign(context.Context, Access, string, string) error {
	f.reassigns++
	return nil
}
