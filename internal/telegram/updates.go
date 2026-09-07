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
func (c *Client) ListenCallbacks(ctx context.Context, handle CallbackHandler, reportError CallbackErrorHandler) {
	c.botMutex.RLock()
	bot := c.bot
	c.botMutex.RUnlock()
	handlerID := bot.RegisterHandler(
		telegrambot.HandlerTypeCallbackQueryData,
		RefreshCallbackData,
		telegrambot.MatchTypeExact,
		func(_ context.Context, _ *telegrambot.Bot, update *models.Update) {
			c.observeUpdate(update)
			if handle == nil || update.CallbackQuery == nil {
				return
			}

			handle(ctx, callbackFromSDK(update.CallbackQuery))
		},
	)
	defer bot.UnregisterHandler(handlerID)

	errorsDone := make(chan struct{})
	go func() {
		defer close(errorsDone)
		c.reportCallbackErrors(ctx, reportError)
	}()
	bot.Start(ctx)
	<-errorsDone
}

// AnswerCallback acknowledges a Telegram callback query.
func (c *Client) AnswerCallback(ctx context.Context, callbackID string, text string) error {
	sdkCtx, call := beginSDKCall(ctx)
	c.botMutex.RLock()
	bot := c.bot
	c.botMutex.RUnlock()
	_, err := bot.AnswerCallbackQuery(sdkCtx, &telegrambot.AnswerCallbackQueryParams{
		CallbackQueryID: callbackID,
		Text:            text,
	})

	return c.normalizeSDKCallError(ctx, call, err)
}

// RemoveReplyMarkup removes the inline keyboard from a message.
func (c *Client) RemoveReplyMarkup(ctx context.Context, chatID int64, messageID int) error {
	sdkCtx, call := beginSDKCall(ctx)
	c.botMutex.RLock()
	bot := c.bot
	c.botMutex.RUnlock()
	_, err := bot.EditMessageReplyMarkup(sdkCtx, &telegrambot.EditMessageReplyMarkupParams{
		ChatID:    chatID,
		MessageID: messageID,
		ReplyMarkup: models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		},
	})

	return c.normalizeSDKCallError(ctx, call, err)
}

func (c *Client) reportCallbackErrors(ctx context.Context, reportError CallbackErrorHandler) {
	for {
		select {
		case <-ctx.Done():
			return
		case sdkErr := <-c.sdkErrors:
			if ctx.Err() != nil {
				return
			}
			if reportError != nil {
				reportError(ctx, c.normalizeSDKError(ctx, sdkErr))
			}
		}
	}
}

func (c *Client) observeSDKUpdate(_ context.Context, _ *telegrambot.Bot, update *models.Update) {
	c.observeUpdate(update)
}

func (c *Client) observeUpdate(update *models.Update) {
	if update != nil {
		c.lastUpdateID.Store(update.ID)
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
