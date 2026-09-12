package app

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

func TestSaveTemplateNormalizesAndReceivesGoal(t *testing.T) {
	book := &fakeTemplateBook{}
	access := Access{UserID: userID, Allow: "task:create"}

	tmpl := domain.Template{
		Goal:       "  File the {{kind}}  ",
		AgentID:    "  018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa  ",
		Outline:    []string{"  Gather items  "},
		Deliveries: []string{"  Mail  "},
		Budget:     10,
	}

	gotID, err := SaveTemplate(context.Background(), book, access, "", tmpl)
	if err != nil {
		t.Fatalf("SaveTemplate() error = %v", err)
	}
	if gotID == "" {
		t.Fatal("expected non-empty template id")
	}
	if book.saveCalls != 1 {
		t.Fatalf("save calls = %d, want 1", book.saveCalls)
	}
	if book.savedTmpl.Goal != "File the {{kind}}" {
		t.Errorf("saved goal = %q, want %q", book.savedTmpl.Goal, "File the {{kind}}")
	}
	if book.savedTmpl.AgentID != "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa" {
		t.Errorf("saved agent id = %q", book.savedTmpl.AgentID)
	}
	if len(book.savedTmpl.Outline) != 1 || book.savedTmpl.Outline[0] != "Gather items" {
		t.Errorf("saved outline = %#v", book.savedTmpl.Outline)
	}
	if len(book.savedTmpl.Deliveries) != 1 || book.savedTmpl.Deliveries[0] != "Mail" {
		t.Errorf("saved deliveries = %#v", book.savedTmpl.Deliveries)
	}

	// Bad slot "{{" does not call the book.
	tmpl.Goal = "{{"
	if _, err := SaveTemplate(context.Background(), book, access, "", tmpl); !errors.Is(err, domain.ErrSlot) {
		t.Fatalf("bad slot error = %v, want %v", err, domain.ErrSlot)
	}
	if book.saveCalls != 1 {
		t.Fatalf("save calls after bad slot = %d, want 1", book.saveCalls)
	}
}

func TestSaveTemplateRejectsNonUUIDAgentID(t *testing.T) {
	book := &fakeTemplateBook{}
	access := Access{UserID: userID, Allow: "task:create"}

	tmpl := domain.Template{
		Goal:    "File the receipts",
		AgentID: "not-a-uuid",
		Budget:  10,
	}

	if _, err := SaveTemplate(context.Background(), book, access, "", tmpl); !errors.Is(err, domain.ErrAgent) {
		t.Fatalf("non-uuid agent id error = %v, want %v", err, domain.ErrAgent)
	}
	if book.saveCalls != 0 {
		t.Fatalf("save calls = %d, want 0", book.saveCalls)
	}
}

func TestSaveTemplateRejectsZeroBudget(t *testing.T) {
	book := &fakeTemplateBook{}
	access := Access{UserID: userID, Allow: "task:create"}

	tmpl := domain.Template{
		Goal:    "File the receipts",
		AgentID: "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa",
		Budget:  0,
	}

	if _, err := SaveTemplate(context.Background(), book, access, "", tmpl); !errors.Is(err, domain.ErrBudget) {
		t.Fatalf("zero budget error = %v, want %v", err, domain.ErrBudget)
	}
	if book.saveCalls != 0 {
		t.Fatalf("save calls = %d, want 0", book.saveCalls)
	}
}

func TestSaveTemplateRejectsBadCaller(t *testing.T) {
	book := &fakeTemplateBook{}
	tmpl := domain.Template{
		Goal:    "File the receipts",
		AgentID: "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa",
		Budget:  10,
	}

	// Missing allow
	if _, err := SaveTemplate(context.Background(), book, Access{UserID: userID}, "", tmpl); !errors.Is(err, ErrAccess) {
		t.Fatalf("empty allow error = %v, want %v", err, ErrAccess)
	}
	// Invalid user id
	if _, err := SaveTemplate(context.Background(), book, Access{UserID: "bad-user", Allow: "task:create"}, "", tmpl); !errors.Is(err, ErrAccess) {
		t.Fatalf("bad user error = %v, want %v", err, ErrAccess)
	}
	// Bad non-empty template id
	if _, err := SaveTemplate(context.Background(), book, Access{UserID: userID, Allow: "task:edit"}, "bad-id", tmpl); !errors.Is(err, ErrTask) {
		t.Fatalf("bad template id error = %v, want %v", err, ErrTask)
	}
	if book.saveCalls != 0 {
		t.Fatalf("save calls = %d, want 0", book.saveCalls)
	}
}

func TestOpenTemplateValidatesCallerAndID(t *testing.T) {
	book := &fakeTemplateBook{
		openTmpl: domain.Template{
			Goal:    "File the receipts",
			AgentID: "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa",
			Budget:  10,
		},
	}
	ctx := context.Background()

	// Bad user
	if _, err := OpenTemplate(ctx, book, Access{UserID: "bad-user", Allow: "task:view"}, "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"); !errors.Is(err, ErrAccess) {
		t.Fatalf("bad user error = %v, want %v", err, ErrAccess)
	}
	// Empty allow
	if _, err := OpenTemplate(ctx, book, Access{UserID: userID}, "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"); !errors.Is(err, ErrAccess) {
		t.Fatalf("empty allow error = %v, want %v", err, ErrAccess)
	}
	// Bad template id
	if _, err := OpenTemplate(ctx, book, Access{UserID: userID, Allow: "task:view"}, "bad-id"); !errors.Is(err, ErrTask) {
		t.Fatalf("bad template id error = %v, want %v", err, ErrTask)
	}
	if book.openCalls != 0 {
		t.Fatalf("open calls = %d, want 0", book.openCalls)
	}

	// Valid open
	got, err := OpenTemplate(ctx, book, Access{UserID: userID, Allow: "task:view"}, "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb")
	if err != nil {
		t.Fatalf("OpenTemplate() error = %v", err)
	}
	if got.Goal != "File the receipts" {
		t.Errorf("got goal = %q", got.Goal)
	}
	if book.openCalls != 1 {
		t.Fatalf("open calls = %d, want 1", book.openCalls)
	}
}

func TestRemoveTemplateValidatesCallerAndID(t *testing.T) {
	book := &fakeTemplateBook{}
	ctx := context.Background()

	// Bad user
	if err := RemoveTemplate(ctx, book, Access{UserID: "bad-user", Allow: "task:delete"}, "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"); !errors.Is(err, ErrAccess) {
		t.Fatalf("bad user error = %v, want %v", err, ErrAccess)
	}
	// Empty allow
	if err := RemoveTemplate(ctx, book, Access{UserID: userID}, "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"); !errors.Is(err, ErrAccess) {
		t.Fatalf("empty allow error = %v, want %v", err, ErrAccess)
	}
	// Bad template id
	if err := RemoveTemplate(ctx, book, Access{UserID: userID, Allow: "task:delete"}, "bad-id"); !errors.Is(err, ErrTask) {
		t.Fatalf("bad template id error = %v, want %v", err, ErrTask)
	}
	if book.removeCalls != 0 {
		t.Fatalf("remove calls = %d, want 0", book.removeCalls)
	}

	// Valid remove
	if err := RemoveTemplate(ctx, book, Access{UserID: userID, Allow: "task:delete"}, "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"); err != nil {
		t.Fatalf("RemoveTemplate() error = %v", err)
	}
	if book.removeCalls != 1 {
		t.Fatalf("remove calls = %d, want 1", book.removeCalls)
	}
}

type fakeTemplateBook struct {
	savedID     string
	savedTmpl   domain.Template
	saveCalls   int
	openCalls   int
	removeCalls int
	openTmpl    domain.Template
	openErr     error
	removeErr   error
}

func (f *fakeTemplateBook) SaveTemplate(_ context.Context, _ Access, id string, tmpl domain.Template) (string, error) {
	f.saveCalls++
	f.savedID = id
	f.savedTmpl = tmpl
	if id == "" {
		return "018f3b2c-7a10-7c4a-8e11-cccccccccccc", nil
	}
	return id, nil
}

func (f *fakeTemplateBook) OpenTemplate(_ context.Context, _ Access, _ string) (domain.Template, error) {
	f.openCalls++
	return f.openTmpl, f.openErr
}

func (f *fakeTemplateBook) RemoveTemplate(_ context.Context, _ Access, _ string) error {
	f.removeCalls++
	return f.removeErr
}
