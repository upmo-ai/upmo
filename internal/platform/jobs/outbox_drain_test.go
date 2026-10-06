package jobs

import (
	"context"
	"testing"
)

func TestRegisterClearsWithNil(t *testing.T) {
	t.Cleanup(ClearHandlers)
	ClearHandlers()
	Register("t", func(context.Context, string, []byte) error { return nil })
	if handlerFor("t") == nil {
		t.Fatal("missing handler")
	}
	Register("t", nil)
	if handlerFor("t") != nil {
		t.Fatal("handler still registered")
	}
}

func TestRegisterIgnoresBlankTopic(t *testing.T) {
	t.Cleanup(ClearHandlers)
	ClearHandlers()
	Register("  ", func(context.Context, string, []byte) error { return nil })
	if handlerFor("  ") != nil || handlerFor("") != nil {
		t.Fatal("blank topic registered")
	}
}

func TestDrainOnceRequiresPool(t *testing.T) {
	_, err := DrainOnce(context.Background(), nil, 1)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestOutboxDrainArgsKind(t *testing.T) {
	if (OutboxDrainArgs{}).Kind() != outboxDrainKind {
		t.Fatalf("kind = %q", (OutboxDrainArgs{}).Kind())
	}
}
