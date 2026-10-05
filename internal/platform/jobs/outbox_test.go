package jobs

import (
	"testing"
)

func TestOutboxInsertValidatesTopic(t *testing.T) {
	t.Parallel()
	err := OutboxInsert(t.Context(), nil, "  ", []byte(`{"a":1}`))
	if err != ErrOutboxTopic {
		t.Fatalf("err = %v, want ErrOutboxTopic", err)
	}
}

func TestOutboxInsertValidatesPayload(t *testing.T) {
	t.Parallel()
	for _, payload := range [][]byte{nil, {}, []byte("not json"), []byte("  ")} {
		err := OutboxInsert(t.Context(), nil, "topic", payload)
		if err != ErrOutboxPayload {
			t.Fatalf("payload %q: err = %v, want ErrOutboxPayload", payload, err)
		}
	}
}
