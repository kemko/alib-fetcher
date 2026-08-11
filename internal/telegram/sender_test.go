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

func Test_Sender_posts_silent_HTML_message_with_preview_disabled(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/bottest-token/sendMessage", request.URL.Path)
		var payload struct {
			ChatID             string          `json:"chat_id"`
			Text               string          `json:"text"`
			ParseMode          string          `json:"parse_mode"`
			ReplyMarkup        json.RawMessage `json:"reply_markup"`
			LinkPreviewOptions struct {
				Disabled bool `json:"is_disabled"`
			} `json:"link_preview_options"`
			DisableNotification bool `json:"disable_notification"`
		}
		assert.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
		assert.Equal(t, "-100123", payload.ChatID)
		assert.Equal(t, "<b>digest</b>", payload.Text)
		assert.Equal(t, "HTML", payload.ParseMode)
		assert.True(t, payload.DisableNotification)
		assert.True(t, payload.LinkPreviewOptions.Disabled)
		assert.Empty(t, payload.ReplyMarkup)
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"ok":true,"result":{}}`))
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
	err = sender.Send(context.Background(), "<b>digest</b>", true, false)

	// Then
	require.NoError(t, err)
}

func Test_Sender_posts_audible_HTML_message_with_notification_enabled(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/bottest-token/sendMessage", request.URL.Path)
		var payload map[string]json.RawMessage
		assert.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
		disableNotification, ok := payload["disable_notification"]
		if !assert.True(t, ok) {
			return
		}
		assert.JSONEq(t, `false`, string(disableNotification))
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"ok":true,"result":{}}`))
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
	err = sender.Send(context.Background(), "<b>digest</b>", false, false)

	// Then
	require.NoError(t, err)
}

func Test_Sender_posts_refresh_button_when_requested(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/bottest-token/sendMessage", request.URL.Path)
		var payload struct {
			ReplyMarkup struct {
				InlineKeyboard [][]struct {
					Text         string `json:"text"`
					CallbackData string `json:"callback_data"`
				} `json:"inline_keyboard"`
			} `json:"reply_markup"`
		}
		assert.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
		if !assert.Len(t, payload.ReplyMarkup.InlineKeyboard, 1) {
			return
		}
		if !assert.Len(t, payload.ReplyMarkup.InlineKeyboard[0], 1) {
			return
		}
		assert.Equal(t, "Обновить", payload.ReplyMarkup.InlineKeyboard[0][0].Text)
		assert.Equal(t, telegram.RefreshCallbackData, payload.ReplyMarkup.InlineKeyboard[0][0].CallbackData)
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"ok":true,"result":{}}`))
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
	err = sender.Send(context.Background(), "<b>digest</b>", false, true)

	// Then
	require.NoError(t, err)
}

func Test_Sender_returns_API_description_on_rejection(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"ok":false,"description":"chat not found"}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	sender, err := telegram.NewSender(telegram.Config{
		APIBase: server.URL,
		Token:   "test-token",
		ChatID:  "missing",
		Timeout: time.Second,
	})
	require.NoError(t, err)

	// When
	err = sender.Send(context.Background(), "digest", false, false)

	// Then
	require.ErrorIs(t, err, telegram.ErrRejected)
	require.Contains(t, err.Error(), "chat not found")
}

func Test_Sender_exposes_Telegram_retry_delay(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, err := writer.Write([]byte(`{
			"ok":false,
			"description":"Too Many Requests: retry after 1",
			"parameters":{"retry_after":1}
		}`))
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
	err = sender.Send(context.Background(), "digest", false, false)

	// Then
	require.ErrorIs(t, err, telegram.ErrRejected)
	var retryable interface {
		RetryAfter() time.Duration
	}
	require.ErrorAs(t, err, &retryable)
	require.Equal(t, time.Second, retryable.RetryAfter())
}

func Test_NewSender_validates_configuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config telegram.Config
	}{
		{
			name: "unsupported scheme",
			config: telegram.Config{
				APIBase: "file:///tmp",
				Token:   "test-token",
				ChatID:  "-100123",
				Timeout: time.Second,
			},
		},
		{
			name: "malformed URL",
			config: telegram.Config{
				APIBase: "://",
				Token:   "test-token",
				ChatID:  "-100123",
				Timeout: time.Second,
			},
		},
		{
			name: "missing host",
			config: telegram.Config{
				APIBase: "https:///telegram",
				Token:   "test-token",
				ChatID:  "-100123",
				Timeout: time.Second,
			},
		},
		{
			name: "missing token",
			config: telegram.Config{
				APIBase: "https://api.telegram.org",
				ChatID:  "-100123",
				Timeout: time.Second,
			},
		},
		{
			name: "missing chat",
			config: telegram.Config{
				APIBase: "https://api.telegram.org",
				Token:   "test-token",
				Timeout: time.Second,
			},
		},
		{
			name: "non-positive timeout",
			config: telegram.Config{
				APIBase: "https://api.telegram.org",
				Token:   "test-token",
				ChatID:  "-100123",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// When
			sender, err := telegram.NewSender(tt.config)

			// Then
			require.Error(t, err)
			require.Nil(t, sender)
		})
	}
}

func Test_Sender_uses_HTTP_status_when_rejection_has_no_description(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadGateway)
		_, err := writer.Write([]byte(`{"ok":false}`))
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
	err = sender.Send(context.Background(), "digest", false, false)

	// Then
	require.ErrorIs(t, err, telegram.ErrRejected)
	require.Contains(t, err.Error(), "502 Bad Gateway")
}

func Test_Sender_returns_decode_error_for_invalid_response(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`not json`))
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
	err = sender.Send(context.Background(), "digest", false, false)

	// Then
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode Telegram response")
}

func Test_Sender_returns_request_error_for_transport_failure(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	sender, err := telegram.NewSender(telegram.Config{
		APIBase: server.URL,
		Token:   "test-token",
		ChatID:  "-100123",
		Timeout: time.Second,
	})
	require.NoError(t, err)
	server.Close()

	// When
	err = sender.Send(context.Background(), "digest", false, false)

	// Then
	require.ErrorIs(t, err, telegram.ErrRequest)
}

func Test_Sender_returns_context_error_when_request_is_canceled(t *testing.T) {
	t.Parallel()

	// Given
	sender, err := telegram.NewSender(telegram.Config{
		APIBase: "https://api.telegram.org",
		Token:   "test-token",
		ChatID:  "-100123",
		Timeout: time.Second,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// When
	err = sender.Send(ctx, "digest", false, false)

	// Then
	require.ErrorIs(t, err, context.Canceled)
}
