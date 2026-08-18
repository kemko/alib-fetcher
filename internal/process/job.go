package process

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/kemko/alib-fetcher/internal/app"
	"github.com/kemko/alib-fetcher/internal/store"
)

type closableState interface {
	app.State
	Close() error
}

type stateOpener func(string, time.Time) (closableState, error)

// ForgetLatest removes the newest state records without starting a digest.
func ForgetLatest(ctx context.Context, statePath string, limit int, logger *slog.Logger) (operationErr error) {
	state, err := store.Open(statePath, time.Now())
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := state.Close(); closeErr != nil {
			operationErr = errors.Join(operationErr, closeErr)
		}
	}()

	deleted, err := state.DeleteLatest(ctx, limit)
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "state.forget_latest.completed",
		slog.Int(logKeyRequested, limit),
		slog.Int(logKeyDeleted, deleted),
	)

	return nil
}

func executeJob(
	ctx context.Context,
	dependencies app.Dependencies,
	statePath string,
	logger *slog.Logger,
) (app.Result, error) {
	return executeJobWithStateOpener(ctx, dependencies, statePath, logger, openState)
}

func openState(path string, migratedAt time.Time) (closableState, error) {
	return store.Open(path, migratedAt)
}

func executeJobWithStateOpener(
	ctx context.Context,
	dependencies app.Dependencies,
	statePath string,
	logger *slog.Logger,
	open stateOpener,
) (result app.Result, jobErr error) {
	logger.InfoContext(ctx, "digest.started")
	state, err := open(statePath, dependencies.Now())
	if err != nil {
		return result, err
	}
	defer func() {
		if closeErr := state.Close(); closeErr != nil {
			jobErr = errors.Join(jobErr, closeErr)
		}
	}()

	dependencies.State = state
	service := app.NewService(dependencies)
	result, jobErr = service.Run(ctx)
	if jobErr != nil {
		return result, jobErr
	}
	logger.InfoContext(ctx, "digest.completed",
		slog.Int(logKeyFetched, result.Fetched),
		slog.Int(logKeyNew, result.New),
		slog.Int(logKeyPruned, result.Pruned),
		slog.Int(logKeySent, result.Sent),
	)

	return result, nil
}
