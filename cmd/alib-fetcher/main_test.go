package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kemko/alib-fetcher/internal/telegram"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_run_wires_once_mode_from_environment(t *testing.T) {
	// Given
	originalCommandLine := flag.CommandLine
	originalArgs := os.Args
	t.Cleanup(func() {
		flag.CommandLine = originalCommandLine
		os.Args = originalArgs
	})
	flag.CommandLine = flag.NewFlagSet("alib-fetcher", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	os.Args = []string{"alib-fetcher", "-once"}

	alibServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, http.MethodGet, request.Method)
		assert.Equal(t, "/tramka.phtml", request.URL.Path)
		assert.Equal(t, "tnew=7", request.URL.RawQuery)
		assert.Equal(t, "alib-fetcher/1.0", request.Header.Get("User-Agent"))
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err := writer.Write([]byte(`<p><b>Книга.</b> Цена: 100 руб. <a href="/book.html"><b>Купить</b></a></p>`))
		assert.NoError(t, err)
	}))
	t.Cleanup(alibServer.Close)

	telegramRequests := make(chan struct{}, 8)
	telegramServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		telegramRequests <- struct{}{}
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "/bottest-token/sendMessage", request.URL.Path)
		body, err := io.ReadAll(request.Body)
		assert.NoError(t, err)
		assert.NotContains(t, string(body), "test-token")

		var payload struct {
			ChatID              string `json:"chat_id"`
			Text                string `json:"text"`
			ParseMode           string `json:"parse_mode"`
			DisableNotification bool   `json:"disable_notification"`
			LinkPreviewOptions  struct {
				Disabled bool `json:"is_disabled"`
			} `json:"link_preview_options"`
			ReplyMarkup struct {
				InlineKeyboard [][]struct {
					Text         string `json:"text"`
					CallbackData string `json:"callback_data"`
				} `json:"inline_keyboard"`
			} `json:"reply_markup"`
		}
		assert.NoError(t, json.Unmarshal(body, &payload))
		assert.Equal(t, "-100123", payload.ChatID)
		assert.Equal(t, "HTML", payload.ParseMode)
		assert.False(t, payload.DisableNotification)
		assert.True(t, payload.LinkPreviewOptions.Disabled)
		assert.Contains(t, payload.Text, "<b>Новые книги на Alib.ru</b>")
		assert.Contains(t, payload.Text, `<a href="`+alibServer.URL+`/book.html">Купить</a>`)
		if assert.Len(t, payload.ReplyMarkup.InlineKeyboard, 1) &&
			assert.Len(t, payload.ReplyMarkup.InlineKeyboard[0], 1) {
			assert.Equal(t, "Обновить", payload.ReplyMarkup.InlineKeyboard[0][0].Text)
			assert.Equal(t, telegram.RefreshCallbackData, payload.ReplyMarkup.InlineKeyboard[0][0].CallbackData)
		}

		writer.Header().Set("Content-Type", "application/json")
		_, err = writer.Write([]byte(`{"ok":true,"result":{}}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(telegramServer.Close)

	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("TELEGRAM_CHAT_ID", "-100123")
	t.Setenv("CRON_SCHEDULE", "0 0 * * *")
	t.Setenv("TIMEZONE", "UTC")
	t.Setenv("RUN_ON_STARTUP", "true")
	t.Setenv("STATE_PATH", filepath.Join(t.TempDir(), "state.db"))
	t.Setenv("ALIB_URL", alibServer.URL+"/tramka.phtml?tnew=7")
	t.Setenv("TELEGRAM_API_BASE", telegramServer.URL)
	t.Setenv("HTTP_TIMEOUT", "2s")
	t.Setenv("MESSAGE_LIMIT", "4000")
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// When
	err := run(logger)

	// Then
	require.NoError(t, err)
	require.Len(t, telegramRequests, 1)
	require.Contains(t, logs.String(), "digest.completed")
	require.False(t, strings.Contains(logs.String(), "test-token"))
}
