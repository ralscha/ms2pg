package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"ms2pg/internal/app"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, os.Args[1:]); err != nil {
		slog.New(slog.NewTextHandler(os.Stderr, nil)).Error("migration failed", "error", err)
		return 1
	}

	return 0
}
