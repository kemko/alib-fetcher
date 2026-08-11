// Package main wires the alib-fetcher service process.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/app"
	"github.com/kemko/alib-fetcher/internal/config"
	"github.com/kemko/alib-fetcher/internal/process"
	"github.com/kemko/alib-fetcher/internal/telegram"
)

const logKeyError = "error"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("service.failed", slog.Any(logKeyError, err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	once := flag.Bool("once", false, "fetch and send one digest, then exit")
	flag.Parse()

	settings, err := config.Load()
	if err != nil {
		return err
	}
	fetcher, err := alib.NewClient(settings.AlibURL, settings.HTTPTimeout)
	if err != nil {
		return err
	}
	sender, err := telegram.NewSender(telegram.Config{
		APIBase: settings.TelegramAPIBase,
		Token:   settings.TelegramToken,
		ChatID:  settings.TelegramChatID,
		Timeout: settings.HTTPTimeout,
	})
	if err != nil {
		return err
	}
	dependencies := app.Dependencies{
		Fetcher:      fetcher,
		Sender:       sender,
		MessageLimit: settings.MessageLimit,
		Now:          time.Now,
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return process.Run(ctx, process.Settings{
		CronSpec:       settings.CronSpec(),
		Location:       settings.Location,
		RunOnStartup:   settings.RunOnStartup,
		StatePath:      settings.StatePath,
		TelegramChatID: settings.TelegramChatID,
	}, dependencies, sender, *once, logger)
}
