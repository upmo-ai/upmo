package notices

import (
	"testing"

	"github.com/google/uuid"
)

func TestInsertValidation(t *testing.T) {
	t.Parallel()
	store := Store{}
	taskID := uuid.MustParse("018f3b2c-7a10-7c4a-8e11-111111111111")
	userID := uuid.MustParse("018f3b2c-7a10-7c4a-8e11-222222222222")

	_, err := store.Insert(t.Context(), userID, KindProposal, "Weekly close", &taskID)
	if err != ErrProposalTask {
		t.Fatalf("proposal with task: err = %v, want ErrProposalTask", err)
	}

	_, err = store.Insert(t.Context(), userID, KindProposal, "   ", nil)
	if err != ErrTitle {
		t.Fatalf("blank title: err = %v, want ErrTitle", err)
	}

	_, err = store.Insert(t.Context(), userID, "scout", "Hello", nil)
	if err != ErrKind {
		t.Fatalf("bad kind: err = %v, want ErrKind", err)
	}
}
