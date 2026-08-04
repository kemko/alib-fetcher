package telegram_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kemmko/alib-fetcher/internal/telegram"
	"github.com/stretchr/testify/require"
)

func Test_Sender_posts_HTML_message_with_preview_disabled(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/bottest-token/sendMessage", request.URL.Path)
		var payload struct {
			ChatID             string `json:"chat_id"`
			Text               string `json:"text"`
			ParseMode          string `json:"parse_mode"`
			LinkPreviewOptions struct {
				Disabled bool `json:"is_disabled"`
			} `json:"link_preview_options"`
		}
		require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
		require.Equal(t, "-100123", payload.ChatID)
		require.Equal(t, "<b>digest</b>", payload.Text)
		require.Equal(t, "HTML", payload.ParseMode)
		require.True(t, payload.LinkPreviewOptions.Disabled)
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"ok":true,"result":{}}`))
		require.NoError(t, err)
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
	err = sender.Send(context.Background(), "<b>digest</b>")

	// Then
	require.NoError(t, err)
}

func Test_Sender_returns_API_description_on_rejection(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"ok":false,"description":"chat not found"}`))
		require.NoError(t, err)
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
	err = sender.Send(context.Background(), "digest")

	// Then
	require.ErrorIs(t, err, telegram.ErrRejected)
	require.Contains(t, err.Error(), "chat not found")
}
