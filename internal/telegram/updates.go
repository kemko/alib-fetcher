package telegram

import (
	"context"
)

// Callback contains one Telegram callback query from an inline button.
type Callback struct {
	ID                  string
	Data                string
	MessageChatUsername string
	MessageChatID       int64
	MessageID           int
}

// PollCallbacks long-polls Telegram callback updates and returns the next offset to persist in memory.
func (s *Sender) PollCallbacks(ctx context.Context, offset int) ([]Callback, int, error) {
	payload := getUpdatesRequest{
		Offset:         offset,
		Timeout:        s.longPollTimeoutSec,
		AllowedUpdates: []string{"callback_query"},
	}
	var updates []update
	if err := s.post(ctx, getUpdatesMethod, payload, &updates); err != nil {
		return nil, offset, err
	}

	callbacks := make([]Callback, 0, len(updates))
	nextOffset := offset
	for _, item := range updates {
		if item.UpdateID >= nextOffset {
			nextOffset = item.UpdateID + 1
		}
		if item.CallbackQuery == nil {
			continue
		}
		callbacks = append(callbacks, Callback{
			ID:                  item.CallbackQuery.ID,
			Data:                item.CallbackQuery.Data,
			MessageChatUsername: item.CallbackQuery.Message.Chat.Username,
			MessageChatID:       item.CallbackQuery.Message.Chat.ID,
			MessageID:           item.CallbackQuery.Message.MessageID,
		})
	}

	return callbacks, nextOffset, nil
}

// AnswerCallback acknowledges a Telegram callback query.
func (s *Sender) AnswerCallback(ctx context.Context, callbackID string, text string) error {
	payload := answerCallbackRequest{
		CallbackQueryID: callbackID,
		Text:            text,
	}

	return s.post(ctx, answerCallbackQueryMethod, payload, nil)
}

// RemoveReplyMarkup removes the inline keyboard from a message.
func (s *Sender) RemoveReplyMarkup(ctx context.Context, chatID int64, messageID int) error {
	payload := editReplyMarkupRequest{
		ChatID:    chatID,
		MessageID: messageID,
		ReplyMarkup: replyMarkup{
			InlineKeyboard: [][]inlineKeyboardButton{},
		},
	}

	return s.post(ctx, editMessageReplyMarkup, payload, nil)
}

type getUpdatesRequest struct {
	AllowedUpdates []string `json:"allowed_updates"`
	Offset         int      `json:"offset"`
	Timeout        int      `json:"timeout"`
}

type answerCallbackRequest struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text"`
}

type editReplyMarkupRequest struct {
	ReplyMarkup replyMarkup `json:"reply_markup"`
	ChatID      int64       `json:"chat_id"`
	MessageID   int         `json:"message_id"`
}

type update struct {
	CallbackQuery *callbackQuery `json:"callback_query"`
	UpdateID      int            `json:"update_id"`
}

type callbackQuery struct {
	ID      string          `json:"id"`
	Data    string          `json:"data"`
	Message callbackMessage `json:"message"`
}

type callbackMessage struct {
	Chat      callbackChat `json:"chat"`
	MessageID int          `json:"message_id"`
}

type callbackChat struct {
	Username string `json:"username"`
	ID       int64  `json:"id"`
}
