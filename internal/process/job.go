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
	return ForgetLatestForChat(ctx, statePath, limit, "", logger)
}

// ForgetLatestForChat removes the newest state records and logs their chat.
func ForgetLatestForChat(
	ctx context.Context,
	statePath string,
	limit int,
	chatID string,
	logger *slog.Logger,
) (operationErr error) {
	state, err := store.Open(statePath, time.Now())
	if err != nil {
		return err
	}
	defer joinCloseError(&operationErr, state)

	deleted, err := state.DeleteLatest(ctx, limit)
	if err != nil {
		return err
	}
	attributes := []any{slog.Int(logKeyRequested, limit), slog.Int(logKeyDeleted, deleted)}
	if chatID != "" {
		attributes = append(attributes, slog.String(logKeyChatID, chatID))
	}
	logger.InfoContext(ctx, "state.forget_latest.completed", attributes...)

	return nil
}

func executeJob(
	ctx context.Context,
	dependencies app.Dependencies,
	statePath string,
	logger *slog.Logger,
) (result app.Result, jobErr error) {
	return executeJobForChat(ctx, dependencies, statePath, "", logger)
}

func executeJobForChat(
	ctx context.Context,
	dependencies app.Dependencies,
	statePath string,
	chatID string,
	logger *slog.Logger,
) (result app.Result, jobErr error) {
	startedAttributes := make([]any, 0, 1)
	if chatID != "" {
		startedAttributes = append(startedAttributes, slog.String(logKeyChatID, chatID))
	}
	logger.InfoContext(ctx, "digest.started", startedAttributes...)
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
	completedAttributes := []any{
		slog.Int(logKeyFetched, result.Fetched),
		slog.Int(logKeyFailed, result.Failed),
		slog.Int(logKeyNew, result.New),
		slog.Int(logKeyPruned, result.Pruned),
		slog.Int(logKeySent, result.Sent),
	}
	if chatID != "" {
		completedAttributes = append(completedAttributes, slog.String(logKeyChatID, chatID))
	}
	logger.InfoContext(ctx, "digest.completed", completedAttributes...)

	return result, nil
}

func joinCloseError(operationErr *error, closer io.Closer) {
	if closeErr := closer.Close(); closeErr != nil {
		*operationErr = errors.Join(*operationErr, closeErr)
	}
}
