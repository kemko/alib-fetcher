package telegram_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kemko/alib-fetcher/internal/telegram"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_Sender_polls_callback_updates(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "/bottest-token/getUpdates", request.URL.Path)
		var payload struct {
			AllowedUpdates []string `json:"allowed_updates"`
			Offset         int      `json:"offset"`
			Timeout        int      `json:"timeout"`
		}
		if !assert.NoError(t, json.NewDecoder(request.Body).Decode(&payload)) {
			return
		}
		assert.Equal(t, 42, payload.Offset)
		assert.Equal(t, 4, payload.Timeout)
		assert.Equal(t, []string{"callback_query"}, payload.AllowedUpdates)
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{
			"ok": true,
			"result": [
				{
					"update_id": 100,
					"callback_query": {
						"id": "callback-1",
						"data": "refresh",
						"message": {
							"message_id": 77,
								"chat": {"id": -100123, "username": "books"}
						}
					}
				},
				{"update_id": 101}
			]
		}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	sender, err := telegram.NewSender(telegram.Config{
		APIBase: server.URL,
		Token:   "test-token",
		ChatID:  "-100123",
		Timeout: 5 * time.Second,
	})
	require.NoError(t, err)

	// When
	callbacks, nextOffset, err := sender.PollCallbacks(context.Background(), 42)

	// Then
	require.NoError(t, err)
	assert.Equal(t, 102, nextOffset)
	assert.Equal(t, []telegram.Callback{
		{
			ID:                  "callback-1",
			Data:                telegram.RefreshCallbackData,
			MessageChatUsername: "books",
			MessageChatID:       -100123,
			MessageID:           77,
		},
	}, callbacks)
}

func Test_Sender_uses_short_poll_when_timeout_cannot_exceed_long_poll(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/bottest-token/getUpdates", request.URL.Path)
		var payload struct {
			AllowedUpdates []string `json:"allowed_updates"`
			Offset         int      `json:"offset"`
			Timeout        int      `json:"timeout"`
		}
		if !assert.NoError(t, json.NewDecoder(request.Body).Decode(&payload)) {
			return
		}
		assert.Equal(t, 0, payload.Timeout)
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"ok": true, "result": []}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	sender, err := telegram.NewSender(telegram.Config{
		APIBase: server.URL,
		Token:   "test-token",
		ChatID:  "-100123",
		Timeout: time.Second,
	})
	require.NoError(t, err)

	// When
	callbacks, nextOffset, err := sender.PollCallbacks(context.Background(), 42)

	// Then
	require.NoError(t, err)
	assert.Empty(t, callbacks)
	assert.Equal(t, 42, nextOffset)
}

func Test_Sender_answers_callback_query(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "/bottest-token/answerCallbackQuery", request.URL.Path)
		var payload struct {
			CallbackQueryID string `json:"callback_query_id"`
			Text            string `json:"text"`
		}
		if !assert.NoError(t, json.NewDecoder(request.Body).Decode(&payload)) {
			return
		}
		assert.Equal(t, "callback-1", payload.CallbackQueryID)
		assert.Equal(t, "Started", payload.Text)
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"ok":true,"result":true}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	sender, err := newTestSender(server.URL)
	require.NoError(t, err)

	// When
	err = sender.AnswerCallback(context.Background(), "callback-1", "Started")

	// Then
	require.NoError(t, err)
}

func Test_Sender_removes_reply_markup(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "/bottest-token/editMessageReplyMarkup", request.URL.Path)
		var payload struct {
			ReplyMarkup struct {
				InlineKeyboard [][]struct{} `json:"inline_keyboard"`
			} `json:"reply_markup"`
			ChatID    int64 `json:"chat_id"`
			MessageID int   `json:"message_id"`
		}
		if !assert.NoError(t, json.NewDecoder(request.Body).Decode(&payload)) {
			return
		}
		assert.Equal(t, int64(-100123), payload.ChatID)
		assert.Equal(t, 77, payload.MessageID)
		if !assert.NotNil(t, payload.ReplyMarkup.InlineKeyboard) {
			return
		}
		assert.Empty(t, payload.ReplyMarkup.InlineKeyboard)
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"ok":true,"result":true}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	sender, err := newTestSender(server.URL)
	require.NoError(t, err)

	// When
	err = sender.RemoveReplyMarkup(context.Background(), -100123, 77)

	// Then
	require.NoError(t, err)
}

func Test_Sender_returns_rejection_for_callback_API_failure(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, err := writer.Write([]byte(`{"ok":false,"description":"query is too old"}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	sender, err := newTestSender(server.URL)
	require.NoError(t, err)

	// When
	err = sender.AnswerCallback(context.Background(), "callback-1", "Started")

	// Then
	require.ErrorIs(t, err, telegram.ErrRejected)
	require.Contains(t, err.Error(), "query is too old")
}

func Test_Sender_returns_decode_error_for_invalid_callback_response(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`not json`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	sender, err := newTestSender(server.URL)
	require.NoError(t, err)

	// When
	callbacks, nextOffset, err := sender.PollCallbacks(context.Background(), 42)

	// Then
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode Telegram response")
	assert.Nil(t, callbacks)
	assert.Equal(t, 42, nextOffset)
}

func Test_Sender_returns_context_error_when_callback_poll_is_canceled(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("request should not be sent")
	}))
	t.Cleanup(server.Close)
	sender, err := newTestSender(server.URL)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// When
	callbacks, nextOffset, err := sender.PollCallbacks(ctx, 42)

	// Then
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, callbacks)
	assert.Equal(t, 42, nextOffset)
}

func newTestSender(apiBase string) (*telegram.Sender, error) {
	return telegram.NewSender(telegram.Config{
		APIBase: apiBase,
		Token:   "test-token",
		ChatID:  "-100123",
		Timeout: time.Second,
	})
}
