package process

import (
	"context"
	"errors"
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
		slog.Int("requested", limit),
		slog.Int("deleted", deleted),
	)

	return nil
}

func executeJob(
	ctx context.Context,
	dependencies app.Dependencies,
	statePath string,
	logger *slog.Logger,
) (jobErr error) {
	logger.InfoContext(ctx, "digest.started")
	state, err := store.Open(statePath, dependencies.Now())
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := state.Close(); closeErr != nil {
			jobErr = errors.Join(jobErr, closeErr)
		}
	}()

	dependencies.State = state
	service := app.NewService(dependencies)
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
