// Package process owns service process lifecycle orchestration.
package process

import (
	"context"
	"log/slog"
	"time"

	"github.com/kemko/alib-fetcher/internal/app"
)

const (
	logKeyError      = "error"
	logKeyFetched    = "fetched"
	logKeyNew        = "new"
	logKeyPruned     = "pruned"
	logKeySchedule   = "cron_schedule"
	logKeySent       = "sent"
	logKeyTimezone   = "timezone"
	logKeyTrigger    = "trigger"
	triggerRefresh   = "refresh"
	triggerScheduled = "scheduled"
	triggerStartup   = "startup"
)

// Settings contains process-level service settings.
type Settings struct {
	Location       *time.Location
	CronSpec       string
	StatePath      string
	TelegramChatID string
	RunOnStartup   bool
}

// Run starts the digest process lifecycle or runs one digest in once mode.
func Run(
	ctx context.Context,
	settings Settings,
	dependencies app.Dependencies,
	callbacks CallbackClient,
	once bool,
	logger *slog.Logger,
) error {
	if once {
		return executeJob(ctx, dependencies, settings.StatePath, logger)
	}

	runner := newDigestRunner(dependencies, settings.StatePath, logger)
	scheduler, err := newScheduler(ctx, settings, runner)
	if err != nil {
		return err
	}

	callbacksDone := startCallbackListening(ctx, callbacks, runner, settings.TelegramChatID, logger)
	logger.InfoContext(ctx, "scheduler.started",
		slog.String(logKeySchedule, settings.CronSpec),
		slog.String(logKeyTimezone, settings.Location.String()),
	)
	runScheduler(ctx, scheduler, func() { runner.runStartup(ctx) }, settings.RunOnStartup)
	logger.InfoContext(ctx, "scheduler.stopped")
	<-callbacksDone
	runner.wait()

	return nil
}
