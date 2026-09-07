package process

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"github.com/kemko/alib-fetcher/internal/telegram"
)

const (
	refreshAlreadyRunningText = "Проверка уже выполняется"
	refreshStartedText        = "Формирование дайджеста запущено"
	refreshUnavailableText    = "Кнопка недоступна"
)

// CallbackClient contains Telegram callback operations used by the process.
type CallbackClient interface {
	ListenCallbacks(ctx context.Context, handle telegram.CallbackHandler, reportError telegram.CallbackErrorHandler)
	AnswerCallback(ctx context.Context, callbackID string, text string) error
	RemoveReplyMarkup(ctx context.Context, chatID int64, messageID int) error
}

type callbackRecipient struct {
	runner *digestRunner
	chatID string
}

type callbackGroup struct {
	client     CallbackClient
	recipients []callbackRecipient
}

func startCallbackGroups(
	ctx context.Context,
	recipients []Recipient,
	runners []*digestRunner,
	logger *slog.Logger,
) <-chan struct{} {
	groups := make([]callbackGroup, 0, len(recipients))
	for index, recipient := range recipients {
		if recipient.Callbacks == nil {
			continue
		}
		groupIndex := -1
		for candidate := range groups {
			if sameCallbackClient(groups[candidate].client, recipient.Callbacks) {
				groupIndex = candidate
				break
			}
		}
		if groupIndex == -1 {
			groups = append(groups, callbackGroup{client: recipient.Callbacks})
			groupIndex = len(groups) - 1
		}
		groups[groupIndex].recipients = append(groups[groupIndex].recipients, callbackRecipient{
			chatID: recipient.ChatID,
			runner: runners[index],
		})
	}

	done := make(chan struct{})
	var listeners sync.WaitGroup
	listeners.Add(len(groups))
	for _, group := range groups {
		go func(group callbackGroup) {
			defer listeners.Done()
			group.client.ListenCallbacks(
				ctx,
				func(callbackCtx context.Context, callback telegram.Callback) {
					handleGroupedCallback(callbackCtx, group, callback, logger)
				},
				func(errorCtx context.Context, err error) {
					logger.ErrorContext(errorCtx, "callback.poll_failed", slog.Any(logKeyError, err))
				},
			)
		}(group)
	}
	go func() {
		listeners.Wait()
		close(done)
	}()

	return done
}

func sameCallbackClient(left CallbackClient, right CallbackClient) bool {
	leftValue := reflect.ValueOf(left)
	rightValue := reflect.ValueOf(right)
	if !leftValue.IsValid() || !rightValue.IsValid() || leftValue.Type() != rightValue.Type() {
		return false
	}
	if !leftValue.Type().Comparable() {
		return false
	}

	return leftValue.Interface() == rightValue.Interface()
}

func handleGroupedCallback(
	ctx context.Context,
	group callbackGroup,
	callback telegram.Callback,
	logger *slog.Logger,
) {
	if callback.Data != telegram.RefreshCallbackData {
		return
	}

	var match *callbackRecipient
	for index := range group.recipients {
		if !matchesExpectedChat(callback, group.recipients[index].chatID) {
			continue
		}
		if match != nil {
			answerRefreshCallback(ctx, group.client, callback.ID, refreshUnavailableText, logger)

			return
		}
		match = &group.recipients[index]
	}
	if match == nil {
		answerRefreshCallback(ctx, group.client, callback.ID, refreshUnavailableText, logger)

		return
	}
	handleRefreshCallback(ctx, group.client, match.runner, callback, logger)
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
	started := runner.tryStartRefresh(ctx, beforeDelivery)
	if started {
		answerRefreshCallback(ctx, callbacks, callback.ID, refreshStartedText, logger)

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
