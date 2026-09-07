package process

import (
	"context"
	"log/slog"
	"sync"

	"github.com/kemko/alib-fetcher/internal/app"
)

type digestRunner struct {
	dependencies app.Dependencies
	logger       *slog.Logger
	statePath    string
	chatID       string
	lock         sync.Mutex
	runs         sync.WaitGroup
	refreshRuns  sync.WaitGroup
}

func newDigestRunner(dependencies app.Dependencies, statePath string, logger *slog.Logger) *digestRunner {
	return newDigestRunnerWithChat(dependencies, statePath, "", logger)
}

func newDigestRunnerForRecipient(recipient Recipient, logger *slog.Logger) *digestRunner {
	return newDigestRunnerWithChat(recipient.Dependencies, recipient.StatePath, recipient.ChatID, logger)
}

func newDigestRunnerWithChat(
	dependencies app.Dependencies,
	statePath string,
	chatID string,
	logger *slog.Logger,
) *digestRunner {
	return &digestRunner{
		dependencies: dependencies,
		logger:       logger,
		statePath:    statePath,
		chatID:       chatID,
	}
}

func (r *digestRunner) runStartup(ctx context.Context) {
	r.runs.Add(1)
	defer r.runs.Done()
	r.lock.Lock()
	defer r.lock.Unlock()

	_, err := r.runLocked(ctx, nil)
	r.logFailure(ctx, triggerStartup, err)
}

func (r *digestRunner) runScheduled(ctx context.Context) {
	r.runs.Add(1)
	defer r.runs.Done()
	if !r.lock.TryLock() {
		return
	}
	defer r.lock.Unlock()

	_, err := r.runLocked(ctx, nil)
	r.logFailure(ctx, triggerScheduled, err)
}

func (r *digestRunner) tryStartRefresh(
	ctx context.Context,
	beforeDelivery func(context.Context) error,
) bool {
	if !r.lock.TryLock() {
		return false
	}

	r.refreshRuns.Add(1)
	go func() {
		defer r.refreshRuns.Done()

		_, err := r.runLocked(ctx, beforeDelivery)
		r.logFailure(ctx, triggerRefresh, err)
		r.lock.Unlock()
	}()

	return true
}

func (r *digestRunner) wait() {
	r.refreshRuns.Wait()
	r.runs.Wait()
}

func (r *digestRunner) runLocked(
	ctx context.Context,
	beforeDelivery func(context.Context) error,
) (app.Result, error) {
	dependencies := r.dependencies
	dependencies.BeforeDelivery = beforeDelivery

	return executeJobForChat(ctx, dependencies, r.statePath, r.chatID, r.logger)
}

func (r *digestRunner) logFailure(ctx context.Context, trigger string, err error) {
	if err != nil {
		attributes := []any{slog.Any(logKeyError, err), slog.String(logKeyTrigger, trigger)}
		if r.chatID != "" {
			attributes = append(attributes, slog.String(logKeyChatID, r.chatID))
		}
		r.logger.ErrorContext(ctx, "digest.failed", attributes...)
	}
}
