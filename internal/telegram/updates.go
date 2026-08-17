package telegram

import (
	"context"

	telegrambot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Callback contains one Telegram callback query from an inline button.
type Callback struct {
	ID                  string
	Data                string
	MessageChatUsername string
	MessageChatID       int64
	MessageID           int
}

// CallbackHandler processes one supported callback query.
type CallbackHandler func(context.Context, Callback)

// CallbackErrorHandler reports one SDK polling error.
type CallbackErrorHandler func(context.Context, error)

// ListenCallbacks runs SDK-managed polling until ctx is canceled.
func (s *Sender) ListenCallbacks(ctx context.Context, handle CallbackHandler, reportError CallbackErrorHandler) {
	handlerID := s.bot.RegisterHandler(
		telegrambot.HandlerTypeCallbackQueryData,
		RefreshCallbackData,
		telegrambot.MatchTypeExact,
		func(handlerCtx context.Context, _ *telegrambot.Bot, update *models.Update) {
			if handle == nil || update.CallbackQuery == nil {
				return
			}

			handle(handlerCtx, callbackFromSDK(update.CallbackQuery))
		},
	)
	defer s.bot.UnregisterHandler(handlerID)

	errorsDone := make(chan struct{})
	go func() {
		defer close(errorsDone)
		s.reportCallbackErrors(ctx, reportError)
	}()
	s.bot.Start(ctx)
	<-errorsDone
}

// AnswerCallback acknowledges a Telegram callback query.
func (s *Sender) AnswerCallback(ctx context.Context, callbackID string, text string) error {
	_, err := s.bot.AnswerCallbackQuery(ctx, &telegrambot.AnswerCallbackQueryParams{
		CallbackQueryID: callbackID,
		Text:            text,
	})

	return s.normalizeSDKError(ctx, err)
}

// RemoveReplyMarkup removes the inline keyboard from a message.
func (s *Sender) RemoveReplyMarkup(ctx context.Context, chatID int64, messageID int) error {
	_, err := s.bot.EditMessageReplyMarkup(ctx, &telegrambot.EditMessageReplyMarkupParams{
		ChatID:    chatID,
		MessageID: messageID,
		ReplyMarkup: models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		},
	})

	return s.normalizeSDKError(ctx, err)
}

func (s *Sender) reportCallbackErrors(ctx context.Context, reportError CallbackErrorHandler) {
	for {
		select {
		case <-ctx.Done():
			return
		case sdkErr := <-s.sdkErrors:
			if ctx.Err() != nil {
				return
			}
			if reportError != nil {
				reportError(ctx, s.normalizeSDKError(ctx, sdkErr))
			}
		}
	}
}

func ignoreSDKUpdate(context.Context, *telegrambot.Bot, *models.Update) {}

func callbackFromSDK(query *models.CallbackQuery) Callback {
	callback := Callback{ID: query.ID, Data: query.Data}
	switch {
	case query.Message.Message != nil:
		callback.MessageChatUsername = query.Message.Message.Chat.Username
		callback.MessageChatID = query.Message.Message.Chat.ID
		callback.MessageID = query.Message.Message.ID
	case query.Message.InaccessibleMessage != nil:
		callback.MessageChatUsername = query.Message.InaccessibleMessage.Chat.Username
		callback.MessageChatID = query.Message.InaccessibleMessage.Chat.ID
		callback.MessageID = query.Message.InaccessibleMessage.MessageID
	}

	return callback
}
