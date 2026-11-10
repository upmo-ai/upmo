package stream_test

import (
	"net/http/httptest"
	"testing"

	"github.com/upmo-ai/upmo/internal/platform/stream"
)

func TestSSEWriter_Frames(t *testing.T) {
	tests := []struct {
		name  string
		write func(stream.SSEWriter) error
		want  string
	}{
		{"hello", stream.SSEWriter.Hello, ": ok\n\nretry: 3000\n\n"},
		{"heartbeat", stream.SSEWriter.Heartbeat, ": hb\n\n"},
		{"event with id", func(s stream.SSEWriter) error { return s.Event("line", "7", map[string]int{"n": 1}) }, "event: line\nid: 7\ndata: {\"n\":1}\n\n"},
		{"event without id", func(s stream.SSEWriter) error { return s.Event("status", "", map[string]int{"n": 1}) }, "event: status\ndata: {\"n\":1}\n\n"},
		{"end", func(s stream.SSEWriter) error { return s.End("finished") }, "event: end\ndata: {\"reason\":\"finished\"}\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			if err := tt.write(stream.NewSSEWriter(rec)); err != nil {
				t.Fatal(err)
			}
			if got := rec.Body.String(); got != tt.want {
				t.Fatalf("frame = %q, want %q", got, tt.want)
			}
		})
	}
}
