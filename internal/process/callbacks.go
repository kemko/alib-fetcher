package process

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/kemko/alib-fetcher/internal/telegram"
)

const (
	refreshAlreadyRunningText = "Проверка уже выполняется"
	refreshUnavailableText    = "Кнопка недоступна"
	refreshStartedText        = "Проверяю новые книги"
)

const (
	callbackPollErrorDelay = 5 * time.Second
	callbackPollIdleDelay  = time.Second
)

// CallbackClient contains Telegram callback operations used by the process.
type CallbackClient interface {
	PollCallbacks(ctx context.Context, offset int) ([]telegram.Callback, int, error)
	AnswerCallback(ctx context.Context, callbackID string, text string) error
	RemoveReplyMarkup(ctx context.Context, chatID int64, messageID int) error
}

func startCallbackPolling(
	ctx context.Context,
	callbacks CallbackClient,
	runner *digestRunner,
	expectedChatID string,
	logger *slog.Logger,
) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		pollRefreshCallbacks(ctx, callbacks, runner, expectedChatID, logger)
	}()

	return done
}

func pollRefreshCallbacks(
	ctx context.Context,
	callbacks CallbackClient,
	runner *digestRunner,
	expectedChatID string,
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
			if !matchesExpectedChat(callback, expectedChatID) {
				answerRefreshCallback(ctx, callbacks, callback.ID, refreshUnavailableText, logger)
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

func matchesExpectedChat(callback telegram.Callback, expectedChatID string) bool {
	if expectedChatID == "" {
		return true
	}
	if numericChatID, err := strconv.ParseInt(expectedChatID, 10, 64); err == nil {
		return callback.MessageChatID == numericChatID
	}

	expectedUsername := strings.TrimPrefix(expectedChatID, "@")

	return expectedUsername != "" && strings.EqualFold(callback.MessageChatUsername, expectedUsername)
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

	answerRefreshCallback(ctx, callbacks, callback.ID, refreshAlreadyRunningText, logger)
}

func answerRefreshCallback(
	ctx context.Context,
	callbacks CallbackClient,
	callbackID string,
	text string,
	logger *slog.Logger,
) {
	if err := callbacks.AnswerCallback(ctx, callbackID, text); err != nil {
		logger.ErrorContext(ctx, "callback.answer_failed", slog.Any(logKeyError, err))
	}
}
