package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/stream"
)

var (
	// ErrStreamPerson is a person who already holds the most streams open.
	ErrStreamPerson = stream.ErrPerson
	// ErrStreamFull is a process that already holds the most streams open.
	ErrStreamFull = stream.ErrFull
	// ErrStreamClosed is a hub that has shut down.
	ErrStreamClosed = stream.ErrClosed
)

// EndAccess is the reason sent as the end event to a person who lost access.
// The other reasons close the stream without an end event, so the browser
// reconnects and resumes. A chat stream is never finished.
const EndAccess = stream.EndAccess

// What a stream event is.
const (
	// EventDelta is a piece of a reply that is still being written.
	EventDelta = "delta"
	// EventStatus is a model that failed before any text, with the next one.
	EventStatus = "status"
	// EventLine is a stored line.
	EventLine = "line"
)

// StreamEvent is one row of a chat's stream. ID is the cursor: a delta's id,
// or for a line its feed id, which is made after every delta of that line.
type StreamEvent struct {
	ID   string
	Kind string
	// LineID, Seq and Body are those of a delta; for a status event LineID is
	// the line the notice belongs to and Notice carries the rest.
	LineID string
	Seq    int
	Body   string
	Notice Notice
	// Line is the stored line of an EventLine.
	Line Line
}

// Tail is what a chat gained after an event id, read in one transaction.
type Tail struct {
	// Events are the deltas and lines with an id after the cursor, in id
	// order, at most the limit asked for.
	Events []StreamEvent
	// More reports events beyond the limit.
	More bool
}

// StreamRows is what the hub reads. Visible is the access check of a stream;
// Newest is the same check and the id of the newest line, for a stream that
// opens; Tail is the one read per chat and notification. All answer ErrGone
// for a chat the access cannot see.
type StreamRows interface {
	Visible(ctx context.Context, access Access, chatID string) error
	Newest(ctx context.Context, access Access, chatID string) (string, error)
	Tail(ctx context.Context, access Access, chatID, after string, limit int) (Tail, error)
}

// StreamFeed announces that a chat has new rows. Listen blocks until ctx
// ends. It calls notify with the chat id of every notification, and resync
// after every (re)connect, because a notification is not durable.
type StreamFeed = stream.Feed

// Renewer resolves a person's access again: fresh access, false when the
// person has lost it for good, and an error when that could not be found out.
type Renewer = stream.Renewer[Access]

// StreamConfig holds the limits of the hub. A zero field takes its default.
type StreamConfig = stream.Config

// StreamMessage is one message for a stream's writer.
type StreamMessage = stream.Message[StreamEvent, struct{}]

// StreamSpec opens a stream. After is the id of the last event the client has;
// the hub sends everything newer. With no After the client has the lines
// stored now, so the stream starts after the newest of them and replays any
// reply under way. Renew may be nil, and then the access given is kept.
type StreamSpec struct {
	ChatID string
	Person string
	Access Access
	Renew  Renewer
	After  string
}

// StreamHub fans chat rows out to the open streams of one process. It reads
// the rows of a chat once for all of that chat's streams. Access is checked
// when a stream opens and again every Refresh.
type StreamHub struct {
	hub  *stream.Hub[Access, StreamEvent, struct{}]
	rows StreamRows
}

// NewStreamHub returns a hub that reads rows and hears of new rows from feed.
// A nil feed hears of none and relies on the replay timer. A nil log logs
// nothing.
func NewStreamHub(rows StreamRows, feed StreamFeed, log *slog.Logger, cfg StreamConfig) *StreamHub {
	return &StreamHub{
		hub:  stream.NewHub[Access, StreamEvent, struct{}]("chat", eventID, chatRows{rows: rows}, feed, log, cfg),
		rows: rows,
	}
}

// eventID is an event's id, the cursor of a chat stream.
func eventID(event StreamEvent) string { return event.ID }

// chatRows reads chat rows for the stream hub. ErrGone and ErrAccess become
// stream.ErrDenied, so the hub holds the stream for an access check.
type chatRows struct{ rows StreamRows }

func (c chatRows) Check(ctx context.Context, access Access, key string) error {
	return denied(c.rows.Visible(ctx, access, key))
}

func (c chatRows) Tail(ctx context.Context, access Access, key, after string, limit int) (stream.Tail[StreamEvent, struct{}], error) {
	tail, err := c.rows.Tail(ctx, access, key, after, limit)
	if err != nil {
		return stream.Tail[StreamEvent, struct{}]{}, denied(err)
	}
	return stream.Tail[StreamEvent, struct{}]{Events: tail.Events, More: tail.More}, nil
}

func denied(err error) error {
	if errors.Is(err, ErrGone) || errors.Is(err, ErrAccess) {
		return stream.ErrDenied
	}
	return err
}

// Heartbeat is the gap between comment lines on a quiet stream.
func (h *StreamHub) Heartbeat() time.Duration { return h.hub.Heartbeat() }

// Lifetime is the longest a stream stays open.
func (h *StreamHub) Lifetime() time.Duration { return h.hub.Lifetime() }

// Open is the number of open streams.
func (h *StreamHub) Open() int { return h.hub.Open() }

// Start runs the hub until ctx ends or Close is called.
func (h *StreamHub) Start(ctx context.Context) { h.hub.Start(ctx) }

// Close closes every open stream without an end event, so each browser
// reconnects, and stops the hub. It is safe to call more than once.
func (h *StreamHub) Close() { h.hub.Close() }

// Subscribe checks that the person can see the chat and opens a stream on it.
// ErrGone is a chat this person cannot see.
func (h *StreamHub) Subscribe(ctx context.Context, spec StreamSpec) (*Stream, error) {
	newest, err := h.rows.Newest(ctx, spec.Access, spec.ChatID)
	if err != nil {
		return nil, err
	}
	if spec.After == "" {
		spec.After = newest
	}
	return h.hub.Subscribe(stream.Spec[Access]{
		Key:    spec.ChatID,
		Person: spec.Person,
		Access: spec.Access,
		Renew:  spec.Renew,
		After:  spec.After,
	})
}

// Stream is one open stream. Its writer takes messages from Out until Closed
// is closed, then calls Release.
type Stream = stream.Stream[Access, StreamEvent, struct{}]
