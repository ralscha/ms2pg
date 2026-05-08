package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"pgloader-go/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, os.Args[1:]); err != nil {
		slog.New(slog.NewTextHandler(os.Stderr, nil)).Error("migration failed", "error", err)
		os.Exit(1)
	}
}
