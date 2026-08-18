package process

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/kemko/alib-fetcher/internal/app"
	"github.com/kemko/alib-fetcher/internal/telegram"
)

const (
	maxCallbackTextRunes      = 200
	refreshAlreadyRunningText = "Проверка уже выполняется"
	refreshNoBooksText        = "Новых книг нет"
	refreshUnavailableText    = "Кнопка недоступна"
)

// CallbackClient contains Telegram callback operations used by the process.
type CallbackClient interface {
	ListenCallbacks(ctx context.Context, handle telegram.CallbackHandler, reportError telegram.CallbackErrorHandler)
	AnswerCallback(ctx context.Context, callbackID string, text string) error
	RemoveReplyMarkup(ctx context.Context, chatID int64, messageID int) error
}

func startCallbackListening(
	ctx context.Context,
	callbacks CallbackClient,
	runner *digestRunner,
	expectedChatID string,
	logger *slog.Logger,
) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		callbacks.ListenCallbacks(
			ctx,
			func(callbackCtx context.Context, callback telegram.Callback) {
				handleCallback(callbackCtx, callbacks, runner, callback, expectedChatID, logger)
			},
			func(errorCtx context.Context, err error) {
				logger.ErrorContext(errorCtx, "callback.poll_failed", slog.Any(logKeyError, err))
			},
		)
	}()

	return done
}

func handleCallback(
	ctx context.Context,
	callbacks CallbackClient,
	runner *digestRunner,
	callback telegram.Callback,
	expectedChatID string,
	logger *slog.Logger,
) {
	if callback.Data != telegram.RefreshCallbackData {
		return
	}
	if !matchesExpectedChat(callback, expectedChatID) {
		answerRefreshCallback(ctx, callbacks, callback.ID, refreshUnavailableText, logger)

		return
	}
	handleRefreshCallback(ctx, callbacks, runner, callback, logger)
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
	started := runner.tryStartRefresh(ctx, beforeDelivery, func(result app.Result, err error) {
		text := ""
		if err != nil {
			text = err.Error()
		} else if result.New == 0 {
			text = refreshNoBooksText
		}
		answerRefreshCallback(ctx, callbacks, callback.ID, text, logger)
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
	if err := callbacks.AnswerCallback(ctx, callbackID, limitCallbackText(text)); err != nil {
		logger.ErrorContext(ctx, "callback.answer_failed", slog.Any(logKeyError, err))
	}
}

func limitCallbackText(text string) string {
	runes := []rune(text)
	if len(runes) <= maxCallbackTextRunes {
		return text
	}

	return string(runes[:maxCallbackTextRunes-1]) + "…"
}
