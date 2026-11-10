package stream

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// Listener holds one connection of its own, outside any pool, that LISTENs on
// a NOTIFY channel. It reconnects with a growing pause when the connection
// drops.
type Listener struct {
	// Config is how to connect. The connection is not pooled and runs no role
	// statement: LISTEN needs no privilege.
	Config *pgx.ConnConfig
	Log    *slog.Logger
	// Channel is the NOTIFY channel to LISTEN on. Its payload is a JSON object.
	Channel string
	// ApplicationName names the connection in pg_stat_activity.
	ApplicationName string
	// Subject is the payload field that holds the key, and the word the log
	// line uses for what is streamed.
	Subject string
	// MinBackoff and MaxBackoff bound the pause before a reconnect. Zero
	// takes 200 ms and 10 s.
	MinBackoff time.Duration
	MaxBackoff time.Duration
}

// keepalive is how long the listener waits for a notification before it
// checks that the connection still answers.
const keepalive = 30 * time.Second

// Listen calls notify with the key of each notification and resync each time
// listening (re)starts. It blocks until ctx ends.
func (l Listener) Listen(ctx context.Context, notify func(key string), resync func()) {
	minPause, maxPause := l.MinBackoff, l.MaxBackoff
	if minPause <= 0 {
		minPause = 200 * time.Millisecond
	}
	if maxPause <= 0 {
		maxPause = 10 * time.Second
	}
	log := l.Log
	if log == nil {
		log = slog.Default()
	}
	pause := minPause
	for ctx.Err() == nil {
		started := time.Now()
		err := l.listenOnce(ctx, notify, resync)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > maxPause {
			pause = minPause
		}
		log.Warn(l.Subject+" stream listener lost its connection", "err", err, "retry_in", pause.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
		}
		pause = min(pause*2, maxPause)
	}
}

func (l Listener) listenOnce(ctx context.Context, notify func(string), resync func()) error {
	cfg := l.Config.Copy()
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["application_name"] = l.ApplicationName
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(ctx, "LISTEN "+l.Channel); err != nil {
		return err
	}
	// Listening has begun, so a notification from here on arrives. Rows
	// written before it are read from the tables.
	resync()
	for {
		wait, cancel := context.WithTimeout(ctx, keepalive)
		note, err := conn.WaitForNotification(wait)
		cancel()
		switch {
		case err == nil:
			var payload map[string]any
			if json.Unmarshal([]byte(note.Payload), &payload) == nil {
				if key, _ := payload[l.Subject].(string); key != "" {
					notify(key)
				}
			}
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, context.DeadlineExceeded):
			pctx, pcancel := context.WithTimeout(ctx, keepalive)
			err := conn.Ping(pctx)
			pcancel()
			if err != nil {
				return err
			}
		default:
			return err
		}
	}
}
