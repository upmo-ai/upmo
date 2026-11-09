package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/stream"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

var (
	// ErrStreamPerson is a person who already holds the most streams open.
	ErrStreamPerson = stream.ErrPerson
	// ErrStreamFull is a process that already holds the most streams open.
	ErrStreamFull = stream.ErrFull
	// ErrStreamClosed is a hub that has shut down.
	ErrStreamClosed = stream.ErrClosed
)

// Why a stream ended. EndFinished is sent to the client as the end event, and
// so is EndAccess. The others close the stream without an end event, so the
// browser reconnects and resumes.
const (
	EndFinished = stream.EndFinished
	EndAccess   = stream.EndAccess
)

// Tail is what a task gained after an event id, read in one transaction.
type Tail struct {
	// Events are the task's events and chat lines with an id after the cursor,
	// in id order, at most the limit asked for, mapped as the detail read model
	// maps them.
	Events []Event
	// State is the stored status, Status what a person sees and Phase the phase
	// shown. State is read before the events, so a Done or Failed State comes
	// with all of the task's events.
	State  domain.Status
	Status domain.Shown
	Phase  domain.Status
	// More reports events beyond the limit.
	More bool
}

// StreamRows is what the hub reads. Find is the access check; Tail is the
// one read per task and notification. Both answer ErrGone for a task the
// access cannot see.
type StreamRows interface {
	Finder
	Tail(ctx context.Context, access Access, taskID, after string, limit int) (Tail, error)
}

// StreamFeed announces that a task has new events. Listen blocks until ctx
// ends. It calls notify with the task id of every notification, and resync
// after every (re)connect, because a notification is not durable.
type StreamFeed = stream.Feed

// Renewer resolves a person's access again: fresh access, false when the
// person has lost it for good (the session ended, the role went), and an
// error when that could not be found out.
type Renewer = stream.Renewer[Access]

// StreamConfig holds the limits of the hub. A zero field takes its default.
type StreamConfig = stream.Config

// StreamKind tells the messages of a stream apart.
type StreamKind = stream.Kind

const (
	// StreamLine is one event.
	StreamLine = stream.KindEvent
	// StreamStatus is the status and phase after the events of a change.
	StreamStatus = stream.KindSummary
	// StreamEnd is the last message of a task that is Done or Failed.
	StreamEnd = stream.KindEnd
)

// statusSummary is what a task's tail summarizes: the status a person sees
// and the phase shown.
type statusSummary struct {
	Status domain.Shown
	Phase  domain.Status
}

// StreamMessage is one message for a stream's writer. Summary is the task's
// status and phase.
type StreamMessage = stream.Message[Event, statusSummary]

// StreamSpec opens a stream. After is the id of the last event the client has;
// the hub sends everything newer. Renew may be nil, and then the access given
// is kept.
type StreamSpec struct {
	TaskID string
	Person string
	Access Access
	Renew  Renewer
	After  string
}

// StreamHub fans task events out to the open streams of one process. It reads
// the rows of a task once for all of that task's streams. Access is checked
// when a stream opens and again every Refresh.
type StreamHub struct {
	hub *stream.Hub[Access, Event, statusSummary]
}

// NewStreamHub returns a hub that reads rows and hears of new events from
// feed. A nil feed hears of none and relies on the replay timer. A nil log
// logs nothing.
func NewStreamHub(rows StreamRows, feed StreamFeed, log *slog.Logger, cfg StreamConfig) *StreamHub {
	return &StreamHub{hub: stream.NewHub[Access, Event, statusSummary]("task", eventID, taskRows{rows: rows}, feed, log, cfg)}
}

// eventID is an event's id, the cursor of a task stream.
func eventID(event Event) string { return event.ID }

// taskRows reads task rows for the stream hub: the access check through
// FindTask, and the tail mapped onto the generic one. ErrGone and ErrAccess
// become stream.ErrDenied, so the hub holds the stream for an access check.
type taskRows struct{ rows StreamRows }

func (t taskRows) Check(ctx context.Context, access Access, key string) error {
	if _, err := FindTask(ctx, t.rows, access, key); err != nil {
		if errors.Is(err, ErrGone) || errors.Is(err, ErrAccess) {
			return stream.ErrDenied
		}
		return err
	}
	return nil
}

func (t taskRows) Tail(ctx context.Context, access Access, key, after string, limit int) (stream.Tail[Event, statusSummary], error) {
	tail, err := t.rows.Tail(ctx, access, key, after, limit)
	if err != nil {
		if errors.Is(err, ErrGone) || errors.Is(err, ErrAccess) {
			return stream.Tail[Event, statusSummary]{}, stream.ErrDenied
		}
		return stream.Tail[Event, statusSummary]{}, err
	}
	return stream.Tail[Event, statusSummary]{
		Events:     tail.Events,
		Summary:    statusSummary{Status: tail.Status, Phase: tail.Phase},
		HasSummary: true,
		Finished:   finished(tail.State),
		More:       tail.More,
	}, nil
}

func finished(state domain.Status) bool {
	return state == domain.Done || state == domain.Failed
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

// Subscribe opens a stream on a task the caller has already checked access
// to. The stream is sent every event after spec.After, then each new one.
func (h *StreamHub) Subscribe(spec StreamSpec) (*Stream, error) {
	return h.hub.Subscribe(stream.Spec[Access]{
		Key:    spec.TaskID,
		Person: spec.Person,
		Access: spec.Access,
		Renew:  spec.Renew,
		After:  spec.After,
	})
}

// Stream is one open stream. Its writer takes messages from Out until Closed
// is closed or an end message arrives, then calls Release.
type Stream = stream.Stream[Access, Event, statusSummary]
