package stream

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
)

// writeDeadline is how long each write to a stream may take. The server's
// WriteTimeout counts from the request header and would cut a long stream off,
// so every write sets a new deadline.
const writeDeadline = 30 * time.Second

// retryMillis is the reconnect delay the browser is told to use.
const retryMillis = 3000

// SSEWriter writes event stream frames. Each write sets a new write deadline
// and is flushed.
type SSEWriter struct {
	w  io.Writer
	rc *http.ResponseController
}

// NewSSEWriter writes to w, which must already carry the stream headers.
func NewSSEWriter(w http.ResponseWriter) SSEWriter {
	return SSEWriter{w: w, rc: http.NewResponseController(w)}
}

// Write sends one raw frame.
func (s SSEWriter) Write(frame string) error {
	if err := s.rc.SetWriteDeadline(time.Now().Add(writeDeadline)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if _, err := io.WriteString(s.w, frame); err != nil {
		return err
	}
	return s.rc.Flush()
}

// Hello opens the stream and tells the browser how long to wait before it
// reconnects.
func (s SSEWriter) Hello() error {
	return s.Write(": ok\n\nretry: " + strconv.Itoa(retryMillis) + "\n\n")
}

// Heartbeat keeps an idle stream from being closed.
func (s SSEWriter) Heartbeat() error {
	return s.Write(": hb\n\n")
}

// Event sends body as the JSON data of a named event. A client that
// reconnects resumes after id; an empty id sends none.
func (s SSEWriter) Event(name, id string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	frame := "event: " + name + "\n"
	if id != "" {
		frame += "id: " + id + "\n"
	}
	return s.Write(frame + "data: " + string(data) + "\n\n")
}

// End tells the client the stream is over and why.
func (s SSEWriter) End(reason string) error {
	return s.Event("end", "", map[string]string{"reason": reason})
}
