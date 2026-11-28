package postgres

import (
	"context"
	"testing"

	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

func TestStepLockedRejectsInvalidActorBeforeQuery(t *testing.T) {
	if _, _, err := stepLocked(context.Background(), nil, "", "invalid", "", domain.Input{}); err == nil {
		t.Fatal("stepLocked accepted invalid actor")
	}
}
