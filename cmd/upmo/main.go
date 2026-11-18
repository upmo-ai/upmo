// Command upmo is the composition root: it hosts every bounded context in
// one process.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/upmo-ai/upmo/pkg/brain"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] != "" && os.Args[1][0] != '-' {
		if err := command(os.Args[1], os.Args[2:]); err != nil {
			slog.Error("brain command failed", "err", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("brain stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return brain.Run(ctx)
}
