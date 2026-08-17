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

type sdkCallbackUpdate struct {
	callback   *Callback
	nextOffset int
}

// PollCallbacks exposes SDK-managed callback polling through the transitional process pull contract.
func (s *Sender) PollCallbacks(ctx context.Context, offset int) ([]Callback, int, error) {
	s.startOnce.Do(func() {
		go s.bot.Start(ctx)
	})

	select {
	case <-ctx.Done():
		return nil, offset, ctx.Err()
	case sdkErr := <-s.sdkErrors:
		return nil, offset, s.normalizeSDKError(ctx, sdkErr)
	case update := <-s.callbackUpdates:
		nextOffset := max(offset, update.nextOffset)
		if update.callback == nil {
			return []Callback{}, nextOffset, nil
		}

		return []Callback{*update.callback}, nextOffset, nil
	}
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

func (s *Sender) handleSDKUpdate(ctx context.Context, _ *telegrambot.Bot, update *models.Update) {
	nextOffset := 0
	maxInt := int64(^uint(0) >> 1)
	if update.ID >= 0 && update.ID < maxInt {
		nextOffset = int(update.ID + 1)
	}
	envelope := sdkCallbackUpdate{nextOffset: nextOffset}
	if update.CallbackQuery != nil {
		callback := callbackFromSDK(update.CallbackQuery)
		envelope.callback = &callback
	}

	select {
	case <-ctx.Done():
	case s.callbackUpdates <- envelope:
	}
}

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
