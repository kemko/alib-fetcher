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
	lock         sync.Mutex
	refreshRuns  sync.WaitGroup
}

func newDigestRunner(dependencies app.Dependencies, statePath string, logger *slog.Logger) *digestRunner {
	return &digestRunner{
		dependencies: dependencies,
		logger:       logger,
		statePath:    statePath,
	}
}

func (r *digestRunner) runStartup(ctx context.Context) {
	r.lock.Lock()
	defer r.lock.Unlock()

	_, err := r.runLocked(ctx, nil)
	r.logFailure(ctx, triggerStartup, err)
}

func (r *digestRunner) runScheduled(ctx context.Context) {
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
	onComplete func(app.Result, error),
) bool {
	if !r.lock.TryLock() {
		return false
	}

	r.refreshRuns.Add(1)
	go func() {
		defer r.refreshRuns.Done()

		result, err := r.runLocked(ctx, beforeDelivery)
		r.logFailure(ctx, triggerRefresh, err)
		r.lock.Unlock()
		if onComplete != nil {
			onComplete(result, err)
		}
	}()

	return true
}

func (r *digestRunner) wait() {
	r.refreshRuns.Wait()
}

func (r *digestRunner) runLocked(
	ctx context.Context,
	beforeDelivery func(context.Context) error,
) (app.Result, error) {
	dependencies := r.dependencies
	dependencies.BeforeDelivery = beforeDelivery

	return executeJob(ctx, dependencies, r.statePath, r.logger)
}

func (r *digestRunner) logFailure(ctx context.Context, trigger string, err error) {
	if err != nil {
		r.logger.ErrorContext(ctx, "digest.failed", slog.Any(logKeyError, err), slog.String(logKeyTrigger, trigger))
	}
}
