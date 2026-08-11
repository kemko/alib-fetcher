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
	"testing"

	"github.com/kemko/alib-fetcher/internal/telegram"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type telegramMessagePayload struct {
	ReplyMarkup         *telegramReplyMarkup `json:"reply_markup"`
	ChatID              string               `json:"chat_id"`
	Text                string               `json:"text"`
	ParseMode           string               `json:"parse_mode"`
	LinkPreviewOptions  linkPreviewOptions   `json:"link_preview_options"`
	DisableNotification bool                 `json:"disable_notification"`
}

type telegramReplyMarkup struct {
	InlineKeyboard [][]telegramInlineKeyboardButton `json:"inline_keyboard"`
}

type telegramInlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type linkPreviewOptions struct {
	Disabled bool `json:"is_disabled"`
}

func Test_run_wires_once_mode_from_environment(t *testing.T) {
	// Given
	useOnceMode(t)

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

		var payload telegramMessagePayload
		assert.NoError(t, json.Unmarshal(body, &payload))
		assert.Equal(t, "-100123", payload.ChatID)
		assert.Equal(t, "HTML", payload.ParseMode)
		assert.False(t, payload.DisableNotification)
		assert.True(t, payload.LinkPreviewOptions.Disabled)
		assert.Contains(t, payload.Text, "<b>Новые книги на Alib.ru</b>")
		assert.Contains(t, payload.Text, `<a href="`+alibServer.URL+`/book.html">Купить</a>`)
		requireRefreshButton(t, payload)

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
	require.NotContains(t, logs.String(), "test-token")
}

func Test_run_sends_only_final_wired_message_with_sound(t *testing.T) {
	// Given
	useOnceMode(t)

	alibServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, http.MethodGet, request.Method)
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err := writer.Write([]byte(`
			<p><b>Первая.</b> Цена: 100 руб. <a href="/book-1.html"><b>Купить</b></a></p>
			<p><b>Вторая.</b> Цена: 200 руб. <a href="/book-2.html"><b>Купить</b></a></p>
			<p><b>Третья.</b> Цена: 300 руб. <a href="/book-3.html"><b>Купить</b></a></p>
		`))
		assert.NoError(t, err)
	}))
	t.Cleanup(alibServer.Close)

	telegramPayloads := make(chan telegramMessagePayload, 8)
	telegramServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "/bottest-token/sendMessage", request.URL.Path)
		body, err := io.ReadAll(request.Body)
		assert.NoError(t, err)
		assert.NotContains(t, string(body), "test-token")

		var payload telegramMessagePayload
		assert.NoError(t, json.Unmarshal(body, &payload))
		telegramPayloads <- payload

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
	t.Setenv("MESSAGE_LIMIT", "150")
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// When
	err := run(logger)

	// Then
	require.NoError(t, err)
	require.Len(t, telegramPayloads, 3)
	payloads := []telegramMessagePayload{
		<-telegramPayloads,
		<-telegramPayloads,
		<-telegramPayloads,
	}
	require.Contains(t, payloads[0].Text, "Первая")
	require.NotContains(t, payloads[0].Text, "Вторая")
	require.Contains(t, payloads[1].Text, "Вторая")
	require.NotContains(t, payloads[1].Text, "Третья")
	require.Contains(t, payloads[2].Text, "Третья")

	for index, payload := range payloads {
		require.Equal(t, "-100123", payload.ChatID)
		require.Equal(t, "HTML", payload.ParseMode)
		require.True(t, payload.LinkPreviewOptions.Disabled)
		require.Contains(t, payload.Text, "<b>Новые книги на Alib.ru</b>")
		if index < len(payloads)-1 {
			require.True(t, payload.DisableNotification)
			require.Nil(t, payload.ReplyMarkup)
			continue
		}

		require.False(t, payload.DisableNotification)
		requireRefreshButton(t, payload)
	}
	require.Contains(t, logs.String(), "digest.completed")
	require.NotContains(t, logs.String(), "test-token")
}

func useOnceMode(t *testing.T) {
	t.Helper()

	originalCommandLine := flag.CommandLine
	originalArgs := os.Args
	t.Cleanup(func() {
		flag.CommandLine = originalCommandLine
		os.Args = originalArgs
	})
	flag.CommandLine = flag.NewFlagSet("alib-fetcher", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	os.Args = []string{"alib-fetcher", "-once"}
}

func requireRefreshButton(t *testing.T, payload telegramMessagePayload) {
	t.Helper()

	require.NotNil(t, payload.ReplyMarkup)
	require.Len(t, payload.ReplyMarkup.InlineKeyboard, 1)
	require.Len(t, payload.ReplyMarkup.InlineKeyboard[0], 1)
	require.Equal(t, "Обновить", payload.ReplyMarkup.InlineKeyboard[0][0].Text)
	require.Equal(t, telegram.RefreshCallbackData, payload.ReplyMarkup.InlineKeyboard[0][0].CallbackData)
}
