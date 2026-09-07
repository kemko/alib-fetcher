// Package process owns service process lifecycle orchestration.
package process

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/kemko/alib-fetcher/internal/app"
)

const (
	logKeyDeleted    = "deleted"
	logKeyChatID     = "chat_id"
	logKeyError      = "error"
	logKeyFetched    = "fetched"
	logKeyFailed     = "failed"
	logKeyNew        = "new"
	logKeyPruned     = "pruned"
	logKeyRequested  = "requested"
	logKeySchedule   = "cron_schedule"
	logKeySent       = "sent"
	logKeyTimezone   = "timezone"
	logKeyTrigger    = "trigger"
	triggerRefresh   = "refresh"
	triggerScheduled = "scheduled"
	triggerStartup   = "startup"
	triggerOnce      = "once"
)

// Settings contains process-level service settings.
type Settings struct {
	Location       *time.Location
	CronSpec       string
	StatePath      string
	TelegramChatID string
	RunOnStartup   bool
}

// Recipient contains the isolated adapters and state path for one chat.
type Recipient struct {
	Dependencies app.Dependencies
	Callbacks    CallbackClient
	ChatID       string
	StatePath    string
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
	return RunRecipients(ctx, settings, []Recipient{{
		ChatID:       settings.TelegramChatID,
		StatePath:    settings.StatePath,
		Dependencies: dependencies,
		Callbacks:    callbacks,
	}}, once, logger)
}

// RunRecipients starts independent digest runners for all recipients.
func RunRecipients(
	ctx context.Context,
	settings Settings,
	recipients []Recipient,
	once bool,
	logger *slog.Logger,
) error {
	if len(recipients) == 0 {
		return errors.New("run process: no recipients configured")
	}
	if once {
		return runOnce(ctx, recipients, logger)
	}

	return runServiceGeneration(ctx, ReloadSnapshot{
		Settings:   settings,
		Recipients: recipients,
	}, logger)
}

func runOnce(ctx context.Context, recipients []Recipient, logger *slog.Logger) error {
	errs := make([]error, len(recipients))
	var runs sync.WaitGroup
	for index, recipient := range recipients {
		runs.Add(1)
		go func() {
			defer runs.Done()
			_, err := executeJobForChat(ctx, recipient.Dependencies, recipient.StatePath, recipient.ChatID, logger)
			if err != nil {
				logger.ErrorContext(ctx, "digest.failed",
					slog.Any(logKeyError, err),
					slog.String(logKeyTrigger, triggerOnce),
					slog.String(logKeyChatID, recipient.ChatID),
				)
				errs[index] = err
			}
		}()
	}
	runs.Wait()

	var joined error
	for _, err := range errs {
		joined = errors.Join(joined, err)
	}

	return joined
}
