package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	telegrambot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/kemko/alib-fetcher/internal/telegram"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type telegramRequest struct {
	Message telegrambot.SendRichMessageParams
	Path    string
}

func Test_run_wires_once_mode_from_environment(t *testing.T) {
	currentYear := time.Now().In(time.UTC).Year()
	// Keep fixtures valid if UTC year changes before run captures the cycle time.
	freshYear := currentYear - 4
	futureYear := currentYear + 2
	testCases := map[string]struct {
		freshBooks string
		freshEmoji string
		configured bool
	}{
		"unset": {},
		"age": {
			freshBooks: "age:5",
			freshEmoji: "✨ ",
			configured: true,
		},
		"since": {
			freshBooks: fmt.Sprintf("since:%d", freshYear),
			freshEmoji: "✨ ",
			configured: true,
		},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			// Given
			useOnceMode(t)
			if testCase.configured {
				t.Setenv("FRESH_BOOKS", testCase.freshBooks)
			} else {
				unsetEnvironment(t, "FRESH_BOOKS")
			}

			alibServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				assert.Equal(t, http.MethodGet, request.Method)
				assert.Equal(t, "/tramka.phtml", request.URL.Path)
				assert.Equal(t, "tnew=7", request.URL.RawQuery)
				assert.Equal(t, "alib-fetcher/1.0", request.Header.Get("User-Agent"))
				writer.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, err := fmt.Fprintf(writer, `<p><b>Горячая книга.</b> М., %d г.<br>
(До заказа внимательно прочтите условия продажи продавца <a href="/bs.php4?bs=BotSad">BS - BotSad</a>, Москва.)
Цена: 3 900 руб. <a href="/hot.html"><b>Купить</b></a><br>
Первая строка содержания.<br>Вторая строка содержания.<br>Состояние: Отличное.<br>
Смотрите: <a href="/foto.php4?id=1">фото</a></p>
<p><b>Свежая книга.</b> М., %d г.<br>
Цена: 500 руб. <a href="/fresh.html"><b>Купить</b></a></p>
<p><b>Будущая книга.</b> М., %d г.<br>
Цена: 700 руб. <a href="/future.html"><b>Купить</b></a></p>`, currentYear, freshYear, futureYear)
				assert.NoError(t, err)
			}))
			t.Cleanup(alibServer.Close)

			telegramRequests := make(chan telegramRequest, 4)
			telegramServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				assert.Equal(t, http.MethodPost, request.Method)
				telegramRequests <- telegramRequest{
					Message: decodeTelegramMessage(t, request),
					Path:    request.URL.Path,
				}

				writer.Header().Set("Content-Type", "application/json")
				_, err := writer.Write([]byte(`{"ok":true,"result":{}}`))
				assert.NoError(t, err)
			}))
			t.Cleanup(telegramServer.Close)

			setRunEnvironment(t, alibServer.URL, telegramServer.URL, filepath.Join(t.TempDir(), "state.db"))
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

			// When
			err := run(logger)

			// Then
			require.NoError(t, err)
			require.Len(t, telegramRequests, 1)
			capturedRequest := <-telegramRequests
			require.Equal(t, "/bottest-token/sendRichMessage", capturedRequest.Path)
			payload := capturedRequest.Message
			require.Equal(t, "-100123", payload.ChatID)
			require.False(t, payload.DisableNotification)
			richHTML := payload.RichMessage.HTML
			firstBook := fmt.Sprintf("🔥 <b>Горячая книга.</b> М., %d г.", currentYear)
			secondBook := testCase.freshEmoji + "<b>Свежая книга.</b>"
			thirdBook := fmt.Sprintf("🛸 <b>Будущая книга.</b> М., %d г.", futureYear)
			firstBookIndex := strings.Index(richHTML, firstBook)
			secondBookIndex := strings.Index(richHTML, secondBook)
			thirdBookIndex := strings.Index(richHTML, thirdBook)
			require.NotEqual(t, -1, firstBookIndex)
			require.NotEqual(t, -1, secondBookIndex)
			require.NotEqual(t, -1, thirdBookIndex)
			require.Less(t, firstBookIndex, secondBookIndex)
			require.Less(t, secondBookIndex, thirdBookIndex)
			require.Equal(t, 2, strings.Count(richHTML, "<hr/>"))
			require.NotContains(t, richHTML[:firstBookIndex], "<hr/>")
			require.Contains(t, richHTML[firstBookIndex:secondBookIndex], "<hr/>")
			require.Contains(t, richHTML[secondBookIndex:thirdBookIndex], "<hr/>")
			require.NotContains(t, richHTML[thirdBookIndex:], "<hr/>")
			require.Contains(t, richHTML, fmt.Sprintf(
				`<p>🔥 <b>Горячая книга.</b> М., %d г.</p><br/>`+
					`<p>Первая строка содержания.<br/>Вторая строка содержания.</p><br/>`+
					`<p>Продавец: <a href="%s/bs.php4?bs=BotSad">BotSad</a>, Москва.`,
				currentYear,
				alibServer.URL,
			))
			require.Contains(t, richHTML, fmt.Sprintf(
				`<p>%s<b>Свежая книга.</b> М., %d г.</p><br/><p>Цена: 500 руб.<br/>Фото: нет</p>`,
				testCase.freshEmoji,
				freshYear,
			))
			require.Contains(t, richHTML, fmt.Sprintf(
				`<p>🛸 <b>Будущая книга.</b> М., %d г.</p><br/><p>Цена: 700 руб.<br/>Фото: нет</p>`,
				futureYear,
			))
			require.Contains(t, richHTML, "<br/>Цена: 3 900 руб.<br/>Состояние: Отличное.<br/>Фото: есть</p>")
			require.NotContains(t, richHTML, "<br/><br/>")
			require.NotContains(t, richHTML, "<br>")
			require.NotRegexp(t, `[\r\n]`, richHTML)
			require.True(
				t,
				strings.HasSuffix(richHTML, `<p><a href="`+alibServer.URL+`/future.html">Купить</a></p>`),
			)
			requireRefreshButton(t, payload)
			require.Contains(t, logs.String(), "digest.completed")
			require.NotContains(t, logs.String(), "test-token")
		})
	}
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

	telegramRequests := make(chan telegramRequest, 8)
	telegramServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, http.MethodPost, request.Method)
		telegramRequests <- telegramRequest{
			Message: decodeTelegramMessage(t, request),
			Path:    request.URL.Path,
		}

		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"ok":true,"result":{}}`))
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
	t.Setenv("MESSAGE_LIMIT", "220")
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// When
	err := run(logger)

	// Then
	require.NoError(t, err)
	require.Len(t, telegramRequests, 3)
	requests := []telegramRequest{
		<-telegramRequests,
		<-telegramRequests,
		<-telegramRequests,
	}
	payloads := []telegrambot.SendRichMessageParams{
		requests[0].Message,
		requests[1].Message,
		requests[2].Message,
	}
	for _, request := range requests {
		require.Equal(t, "/bottest-token/sendRichMessage", request.Path)
	}
	require.Contains(t, payloads[0].RichMessage.HTML, "Первая")
	require.NotContains(t, payloads[0].RichMessage.HTML, "Вторая")
	require.Contains(t, payloads[1].RichMessage.HTML, "Вторая")
	require.NotContains(t, payloads[1].RichMessage.HTML, "Третья")
	require.Contains(t, payloads[2].RichMessage.HTML, "Третья")

	for index, payload := range payloads {
		require.Equal(t, "-100123", payload.ChatID)
		require.NotContains(t, payload.RichMessage.HTML, "<br>")
		require.NotRegexp(t, `[\r\n]`, payload.RichMessage.HTML)
		if index == 0 {
			require.Contains(t, payload.RichMessage.HTML, "<b>Новые книги на Alib.ru</b>")
		} else {
			require.NotContains(t, payload.RichMessage.HTML, "<b>Новые книги на Alib.ru</b>")
		}
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

func requireRefreshButton(t *testing.T, payload telegrambot.SendRichMessageParams) {
	t.Helper()

	replyMarkup, ok := payload.ReplyMarkup.(models.InlineKeyboardMarkup)
	require.True(t, ok)
	require.Len(t, replyMarkup.InlineKeyboard, 1)
	require.Len(t, replyMarkup.InlineKeyboard[0], 1)
	require.Equal(t, "Обновить", replyMarkup.InlineKeyboard[0][0].Text)
	require.Equal(t, telegram.RefreshCallbackData, replyMarkup.InlineKeyboard[0][0].CallbackData)
}

func decodeTelegramMessage(t *testing.T, request *http.Request) telegrambot.SendRichMessageParams {
	t.Helper()
	body, err := io.ReadAll(request.Body)
	require.NoError(t, err)
	require.NotContains(t, string(body), "test-token")
	request.Body = io.NopCloser(bytes.NewReader(body))
	require.NoError(t, request.ParseMultipartForm(1<<20))

	payload := telegrambot.SendRichMessageParams{
		ChatID:              request.FormValue("chat_id"),
		DisableNotification: request.FormValue("disable_notification") == "true",
	}
	require.NoError(t, json.Unmarshal([]byte(request.FormValue("rich_message")), &payload.RichMessage))
	if replyMarkup := request.FormValue("reply_markup"); replyMarkup != "" {
		var inlineKeyboard models.InlineKeyboardMarkup
		require.NoError(t, json.Unmarshal([]byte(replyMarkup), &inlineKeyboard))
		payload.ReplyMarkup = inlineKeyboard
	}

	return payload
}

func setRunEnvironment(t *testing.T, alibURL, telegramAPIBase, statePath string) {
	t.Helper()

	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("TELEGRAM_CHAT_ID", "-100123")
	t.Setenv("CRON_SCHEDULE", "0 0 * * *")
	t.Setenv("TIMEZONE", "UTC")
	t.Setenv("RUN_ON_STARTUP", "true")
	t.Setenv("STATE_PATH", statePath)
	t.Setenv("ALIB_URL", alibURL+"/tramka.phtml?tnew=7")
	t.Setenv("TELEGRAM_API_BASE", telegramAPIBase)
	t.Setenv("HTTP_TIMEOUT", "2s")
	t.Setenv("MESSAGE_LIMIT", "4000")
}

func unsetEnvironment(t *testing.T, key string) {
	t.Helper()

	t.Setenv(key, "")
	require.NoError(t, os.Unsetenv(key))
}
