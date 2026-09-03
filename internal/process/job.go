package process

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/kemko/alib-fetcher/internal/app"
	"github.com/kemko/alib-fetcher/internal/store"
)

// ForgetLatest removes the newest state records without starting a digest.
func ForgetLatest(ctx context.Context, statePath string, limit int, logger *slog.Logger) (operationErr error) {
	state, err := store.Open(statePath, time.Now())
	if err != nil {
		return err
	}
	defer joinCloseError(&operationErr, state)

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
) (result app.Result, jobErr error) {
	logger.InfoContext(ctx, "digest.started")
	state, err := store.Open(statePath, dependencies.Now())
	if err != nil {
		return result, err
	}
	defer joinCloseError(&jobErr, state)

	dependencies.State = state
	service := app.NewService(dependencies)
	result, jobErr = service.Run(ctx)
	if jobErr != nil {
		return result, jobErr
	}
	logger.InfoContext(ctx, "digest.completed",
		slog.Int(logKeyFetched, result.Fetched),
		slog.Int(logKeyFailed, result.Failed),
		slog.Int(logKeyNew, result.New),
		slog.Int(logKeyPruned, result.Pruned),
		slog.Int(logKeySent, result.Sent),
	)

	return result, nil
}

func joinCloseError(operationErr *error, closer io.Closer) {
	if closeErr := closer.Close(); closeErr != nil {
		*operationErr = errors.Join(*operationErr, closeErr)
	}
}
