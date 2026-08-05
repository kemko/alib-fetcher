// Package main wires the alib-fetcher service process.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kemmko/alib-fetcher/internal/alib"
	"github.com/kemmko/alib-fetcher/internal/app"
	"github.com/kemmko/alib-fetcher/internal/config"
	"github.com/kemmko/alib-fetcher/internal/store"
	"github.com/kemmko/alib-fetcher/internal/telegram"

	"github.com/robfig/cron/v3"
)

const (
	logKeyError    = "error"
	logKeyFetched  = "fetched"
	logKeyNew      = "new"
	logKeyPruned   = "pruned"
	logKeyRunAt    = "run_at"
	logKeySent     = "sent"
	logKeyTimezone = "timezone"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("service.failed", slog.Any(logKeyError, err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) (runErr error) {
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
	state, err := store.Open(settings.StatePath, time.Now())
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := state.Close(); closeErr != nil {
			runErr = errors.Join(runErr, closeErr)
		}
	}()

	service := app.NewService(app.Dependencies{
		Fetcher:      fetcher,
		State:        state,
		Sender:       sender,
		MessageLimit: settings.MessageLimit,
		Now:          time.Now,
	})
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if *once {
		return executeJob(ctx, service, logger)
	}

	scheduler := cron.New(
		cron.WithLocation(settings.Location),
		cron.WithChain(cron.SkipIfStillRunning(cron.DiscardLogger)),
	)
	if _, scheduleErr := scheduler.AddFunc(settings.CronSpec(), func() {
		if jobErr := executeJob(ctx, service, logger); jobErr != nil {
			logger.ErrorContext(ctx, "digest.failed", slog.Any(logKeyError, jobErr))
		}
	}); scheduleErr != nil {
		return fmt.Errorf("schedule digest: %w", scheduleErr)
	}

	scheduler.Start()
	logger.InfoContext(ctx, "scheduler.started",
		slog.String(logKeyRunAt, settings.CronSpec()),
		slog.String(logKeyTimezone, settings.Location.String()),
	)
	<-ctx.Done()
	<-scheduler.Stop().Done()
	logger.Info("scheduler.stopped")

	return nil
}

func executeJob(ctx context.Context, service *app.Service, logger *slog.Logger) error {
	logger.InfoContext(ctx, "digest.started")
	result, err := service.Run(ctx)
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "digest.completed",
		slog.Int(logKeyFetched, result.Fetched),
		slog.Int(logKeyNew, result.New),
		slog.Int(logKeyPruned, result.Pruned),
		slog.Int(logKeySent, result.Sent),
	)

	return nil
}
