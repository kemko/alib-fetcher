// Package process owns service process lifecycle orchestration.
package process

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kemko/alib-fetcher/internal/app"
	"github.com/kemko/alib-fetcher/internal/telegram"
)

const (
	logKeyError        = "error"
	logKeyFetched      = "fetched"
	logKeyNew          = "new"
	logKeyPruned       = "pruned"
	logKeySchedule     = "cron_schedule"
	logKeySent         = "sent"
	logKeyTimezone     = "timezone"
	logKeyTrigger      = "trigger"
	logKeyUpdateOffset = "update_offset"
	triggerRefresh     = "refresh"
	triggerScheduled   = "scheduled"
	triggerStartup     = "startup"
)

const (
	refreshAlreadyRunningText = "Проверка уже выполняется"
	refreshStartedText        = "Проверяю новые книги"
)

const (
	callbackPollErrorDelay = 5 * time.Second
	callbackPollIdleDelay  = time.Second
)

// Settings contains process-level service settings.
type Settings struct {
	Location     *time.Location
	CronSpec     string
	StatePath    string
	RunOnStartup bool
}

// CallbackClient contains Telegram callback operations used by the process.
type CallbackClient interface {
	PollCallbacks(ctx context.Context, offset int) ([]telegram.Callback, int, error)
	AnswerCallback(ctx context.Context, callbackID string, text string) error
	RemoveReplyMarkup(ctx context.Context, chatID int64, messageID int) error
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

	callbacksDone := startCallbackPolling(ctx, callbacks, runner, logger)
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

func startCallbackPolling(
	ctx context.Context,
	callbacks CallbackClient,
	runner *digestRunner,
	logger *slog.Logger,
) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		pollRefreshCallbacks(ctx, callbacks, runner, logger)
	}()

	return done
}

func pollRefreshCallbacks(
	ctx context.Context,
	callbacks CallbackClient,
	runner *digestRunner,
	logger *slog.Logger,
) {
	offset := 0
	for ctx.Err() == nil {
		items, nextOffset, err := callbacks.PollCallbacks(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.ErrorContext(ctx, "callback.poll_failed",
				slog.Any(logKeyError, err),
				slog.Int(logKeyUpdateOffset, offset),
			)
			if !waitForCallbackPoll(ctx, callbackPollErrorDelay) {
				return
			}
			continue
		}
		offset = nextOffset
		if len(items) == 0 {
			if !waitForCallbackPoll(ctx, callbackPollIdleDelay) {
				return
			}
			continue
		}
		for _, callback := range items {
			if callback.Data != telegram.RefreshCallbackData {
				continue
			}
			handleRefreshCallback(ctx, callbacks, runner, callback, logger)
		}
	}
}

func waitForCallbackPoll(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func handleRefreshCallback(
	ctx context.Context,
	callbacks CallbackClient,
	runner *digestRunner,
	callback telegram.Callback,
	logger *slog.Logger,
) {
	beforeDelivery := func(runCtx context.Context) error {
		if err := callbacks.RemoveReplyMarkup(runCtx, callback.MessageChatID, callback.MessageID); err != nil {
			return fmt.Errorf("remove refresh button: %w", err)
		}

		return nil
	}
	started := runner.tryStartRefresh(ctx, beforeDelivery, func(runCtx context.Context) error {
		return callbacks.AnswerCallback(runCtx, callback.ID, refreshStartedText)
	})
	if started {
		return
	}

	if err := callbacks.AnswerCallback(ctx, callback.ID, refreshAlreadyRunningText); err != nil {
		logger.ErrorContext(ctx, "callback.answer_failed", slog.Any(logKeyError, err))
	}
}
