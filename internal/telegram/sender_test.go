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
			ChatID              string `json:"chat_id"`
			Text                string `json:"text"`
			ParseMode           string `json:"parse_mode"`
			DisableNotification bool   `json:"disable_notification"`
			LinkPreviewOptions  struct {
				Disabled bool `json:"is_disabled"`
			} `json:"link_preview_options"`
		}
		assert.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
		assert.Equal(t, "-100123", payload.ChatID)
		assert.Equal(t, "<b>digest</b>", payload.Text)
		assert.Equal(t, "HTML", payload.ParseMode)
		assert.True(t, payload.DisableNotification)
		assert.True(t, payload.LinkPreviewOptions.Disabled)
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
	err = sender.Send(context.Background(), "<b>digest</b>", true)

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
	err = sender.Send(context.Background(), "digest", false)

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
	err = sender.Send(context.Background(), "digest", false)

	// Then
	require.ErrorIs(t, err, telegram.ErrRejected)
	var retryable interface {
		RetryAfter() time.Duration
	}
	require.ErrorAs(t, err, &retryable)
	require.Equal(t, time.Second, retryable.RetryAfter())
}
