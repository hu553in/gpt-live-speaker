// Command gpt-live-speaker is a headless voice speaker: it waits for a wake word, then talks to
// OpenAI GPT-Live through a USB speakerphone until the conversation goes quiet.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"gpt-live-speaker/internal/config"
	"gpt-live-speaker/internal/speaker"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx)
	stop()
	if err != nil {
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.ErrorContext(ctx, "invalid configuration", "error", err)
		return err
	}
	logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	if err = speaker.Run(ctx, logger, cfg); err != nil {
		logger.ErrorContext(ctx, "speaker stopped", "error", err)
	}
	return err
}
