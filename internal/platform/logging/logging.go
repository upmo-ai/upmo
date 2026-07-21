// Package logging builds the structured JSON logger every process uses.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// New returns a JSON slog.Logger writing to w at the given level.
// An unknown level falls back to info.
func New(w io.Writer, level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: l}))
}
