package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	telegrambot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/config"
	"github.com/kemko/alib-fetcher/internal/store"
	"github.com/kemko/alib-fetcher/internal/telegram"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	xhtml "golang.org/x/net/html"
)

type telegramRequest struct {
	Message telegrambot.SendRichMessageParams
	Path    string
}

type alibRequest struct {
	Path     string
	RawQuery string
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
Смотрите: <a href="/foto.php4?id=1">Обложка</a> - <a href="foto.php4?id=2"></a> - <a href="/foto.php4?id=1">Повтор</a></p>
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

			setRunEnvironment(t, telegramServer.URL, filepath.Join(t.TempDir(), "state.db"))
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

			// When
			err := runWithAlibURLs(t, logger, alibServer.URL+"/tramka.phtml?tnew=7")

			// Then
			require.NoError(t, err)
			require.Len(t, telegramRequests, 1)
			capturedRequest := <-telegramRequests
			require.Equal(t, "/bottest-token/sendRichMessage", capturedRequest.Path)
			payload := capturedRequest.Message
			require.Equal(t, "-100123", payload.ChatID)
			require.False(t, payload.DisableNotification)
			richHTML := payload.RichMessage.HTML
			firstBook := fmt.Sprintf("🛸 <b>Будущая книга.</b> М., %d г.", futureYear)
			secondBook := fmt.Sprintf("🔥 <b>Горячая книга.</b> М., %d г.", currentYear)
			thirdBook := testCase.freshEmoji + "<b>Свежая книга.</b>"
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
				`🔥 <b>Горячая книга.</b> М., %d г.<br/><br/>`+
					`Первая строка содержания.<br/>Вторая строка содержания.<br/><br/>`+
					`Продавец: <a href="%s/bs.php4?bs=BotSad">BotSad</a>, Москва.`,
				currentYear,
				alibServer.URL,
			))
			require.Contains(t, richHTML, fmt.Sprintf(
				`%s<b>Свежая книга.</b> М., %d г.<br/><br/>Цена: 500 руб.`,
				testCase.freshEmoji,
				freshYear,
			))
			require.Contains(t, richHTML, fmt.Sprintf(
				`🛸 <b>Будущая книга.</b> М., %d г.<br/><br/>Цена: 700 руб.`,
				futureYear,
			))
			require.Contains(t, richHTML, fmt.Sprintf(
				`<br/>Цена: 3 900 руб.<br/>Состояние: Отличное.<br/>Смотрите: `+
					`<a href="%s/foto.php4?id=1">Обложка</a> - `+
					`<a href="%s/foto.php4?id=2">фото</a> - `+
					`<a href="%s/foto.php4?id=1">Повтор</a>`,
				alibServer.URL,
				alibServer.URL,
				alibServer.URL,
			))
			require.NotContains(t, richHTML, "<tg-slideshow>")
			require.NotContains(t, richHTML, "<img ")
			require.NotContains(t, richHTML, "<p>")
			require.NotContains(t, richHTML, "<br>")
			require.NotRegexp(t, `[\r\n]`, richHTML)
			require.True(
				t,
				strings.HasSuffix(richHTML, `<a href="`+alibServer.URL+`/fresh.html">Купить</a>`),
			)
			requireRefreshButton(t, payload)
			require.Contains(t, logs.String(), "digest.completed")
			require.NotContains(t, logs.String(), "test-token")
		})
	}
}

func Test_run_once_sends_truncated_description_through_rich_message(t *testing.T) {
	// Given
	useOnceMode(t)
	const messageLimit = 180
	const buyPath = "/long-description.html"
	longDescription := strings.Repeat("длинное описание ", 100)
	alibServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, http.MethodGet, request.Method)
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err := fmt.Fprintf(writer, `<p><b>Книга с длинным описанием.</b> М., 2026 г.<br>
Цена: 500 руб. <a href="%s"><b>Купить</b></a><br>
%s</p>`, buyPath, longDescription)
		assert.NoError(t, err)
	}))
	t.Cleanup(alibServer.Close)

	telegramRequests := make(chan telegramRequest, 2)
	telegramServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		telegramRequests <- telegramRequest{Message: decodeTelegramMessage(t, request), Path: request.URL.Path}
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"ok":true,"result":{}}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(telegramServer.Close)

	statePath := filepath.Join(t.TempDir(), "state.db")
	setRunEnvironment(t, telegramServer.URL, statePath)
	t.Setenv("MESSAGE_LIMIT", strconv.Itoa(messageLimit))
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// When
	err := runWithAlibURLs(t, logger, alibServer.URL)

	// Then
	require.NoError(t, err)
	require.Len(t, telegramRequests, 2)
	requests := []telegramRequest{<-telegramRequests, <-telegramRequests}
	var listingMessage telegramRequest
	for _, request := range requests {
		require.Equal(t, "/bottest-token/sendRichMessage", request.Path)
		require.LessOrEqual(t, displayedRuneCount(t, request.Message.RichMessage.HTML), messageLimit)
		if strings.Contains(request.Message.RichMessage.HTML, "…") {
			listingMessage = request
		}
	}
	require.NotEmpty(t, listingMessage.Message.RichMessage.HTML)
	require.Contains(t, listingMessage.Message.RichMessage.HTML, "…")
	require.NotContains(t, listingMessage.Message.RichMessage.HTML, longDescription)
	require.Contains(t, logs.String(), `"fetched":1`)
	require.Contains(t, logs.String(), `"new":1`)
	require.Contains(t, logs.String(), `"failed":0`)
	require.Contains(t, logs.String(), `"sent":1`)

	book := alib.Book{BuyURL: alibServer.URL + buyPath}
	state, err := store.Open(statePath, time.Now())
	require.NoError(t, err)
	existing, err := state.Existing(context.Background(), []alib.Book{book})
	require.NoError(t, err)
	require.Equal(t, []bool{true}, existing)
	pending, err := state.Pending(context.Background())
	require.NoError(t, err)
	require.Empty(t, pending)
	require.NoError(t, state.Close())
}

func Test_run_once_uses_default_rich_message_limit_and_listing_block_chunks(t *testing.T) {
	// Given
	useOnceMode(t)
	alibServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, http.MethodGet, request.Method)
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		for index := range 251 {
			_, err := fmt.Fprintf(writer, `<p><b>Книга %d.</b> Цена: 100 руб. <a href="/book-%d.html"><b>Купить</b></a></p>`, index, index)
			assert.NoError(t, err)
		}
	}))
	t.Cleanup(alibServer.Close)

	telegramRequests := make(chan telegramRequest, 2)
	telegramServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		telegramRequests <- telegramRequest{Message: decodeTelegramMessage(t, request), Path: request.URL.Path}
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"ok":true,"result":{}}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(telegramServer.Close)

	statePath := filepath.Join(t.TempDir(), "state.db")
	setRunEnvironment(t, telegramServer.URL, statePath)
	unsetEnvironment(t, "MESSAGE_LIMIT")

	// When
	err := runWithAlibURLs(t, slog.New(slog.DiscardHandler), alibServer.URL)

	// Then
	require.NoError(t, err)
	require.Len(t, telegramRequests, 2)
	requests := []telegramRequest{<-telegramRequests, <-telegramRequests}
	require.Equal(t, "/bottest-token/sendRichMessage", requests[0].Path)
	require.Equal(t, "/bottest-token/sendRichMessage", requests[1].Path)
	require.Equal(t, 250, strings.Count(requests[0].Message.RichMessage.HTML, "<hr/>")+1)
	require.Equal(t, 1, strings.Count(requests[1].Message.RichMessage.HTML, "<b>Книга 250.</b>"))
	for _, request := range requests {
		require.LessOrEqual(t, displayedRuneCount(t, request.Message.RichMessage.HTML), 32000)
		require.NotContains(t, request.Path, "sendMessage")
	}
}

func Test_run_rejects_non_positive_forget_latest(t *testing.T) {
	for _, value := range []string{"0", "-1"} {
		t.Run(value, func(t *testing.T) {
			// Given
			useCommandLine(t, "-forget-latest", value)

			// When
			err := run(slog.New(slog.DiscardHandler))

			// Then
			require.ErrorContains(t, err, "-forget-latest must be positive")
		})
	}
}

func Test_forgetLatestOption_rejects_malformed_values(t *testing.T) {
	t.Parallel()

	testCases := map[string]struct {
		wantError string
		arguments []string
	}{
		"non-numeric": {
			arguments: []string{"-forget-latest", "six"},
			wantError: "-forget-latest must be an integer",
		},
		"overflowing": {
			arguments: []string{"-forget-latest", strings.Repeat("9", 100)},
			wantError: "-forget-latest must be an integer",
		},
		"missing": {
			arguments: []string{"-forget-latest"},
			wantError: "flag needs an argument",
		},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// Given
			flags := flag.NewFlagSet("alib-fetcher", flag.ContinueOnError)
			flags.SetOutput(io.Discard)
			var option forgetLatestOption
			flags.Var(&option, "forget-latest", "delete the latest state records, then exit")

			// When
			err := flags.Parse(testCase.arguments)

			// Then
			require.ErrorContains(t, err, testCase.wantError)
			require.False(t, option.set)
		})
	}
}

func Test_run_rejects_forget_latest_with_once(t *testing.T) {
	// Given
	useCommandLine(t, "-once", "-forget-latest", "1")

	// When
	err := run(slog.New(slog.DiscardHandler))

	// Then
	require.ErrorContains(t, err, "-forget-latest is incompatible with -once")
}

func Test_run_forget_latest_only_requires_state_path(t *testing.T) {
	// Given
	useCommandLine(t, "-forget-latest", "1")
	statePath := filepath.Join(t.TempDir(), "state.db")
	setEnvironmentAbsentDigestConfiguration(t)
	t.Setenv("STATE_PATH", statePath)
	state, err := store.Open(statePath, time.Now())
	require.NoError(t, err)
	book := alib.Book{BuyURL: "https://example.com/book"}
	_, err = state.RecordDiscovered(context.Background(), []alib.Book{book}, time.Now())
	require.NoError(t, err)
	require.NoError(t, state.Close())

	// When
	err = run(slog.New(slog.DiscardHandler))

	// Then
	require.NoError(t, err)
	reopened, err := store.Open(statePath, time.Now())
	require.NoError(t, err)
	pending, err := reopened.Pending(context.Background())
	require.NoError(t, err)
	require.NoError(t, reopened.Close())
	require.Empty(t, pending)
}

func Test_run_forget_latest_documented_cli_scenario_deletes_latest_records_without_http(t *testing.T) {
	// Given
	statePath := filepath.Join(t.TempDir(), "state.db")
	setEnvironmentAbsentDigestConfiguration(t)
	t.Setenv("STATE_PATH", statePath)
	// The Telegram API is intentionally unreachable: maintenance mode must not
	// construct the Alib or Telegram adapters that would use it.
	t.Setenv("TELEGRAM_API_BASE", "http://127.0.0.1:1")

	books := make([]alib.Book, 8)
	for index := range books {
		books[index] = alib.Book{BuyURL: fmt.Sprintf("https://example.com/book-%d", index)}
	}
	state, err := store.Open(statePath, time.Now())
	require.NoError(t, err)
	_, err = state.RecordDiscovered(context.Background(), books, time.Now())
	require.NoError(t, err)
	require.NoError(t, state.Close())
	useCommandLine(t, "-forget-latest", "6")

	// When
	err = run(slog.New(slog.DiscardHandler))

	// Then
	require.NoError(t, err)
	reopened, err := store.Open(statePath, time.Now())
	require.NoError(t, err)
	pending, err := reopened.Pending(context.Background())
	require.NoError(t, err)
	require.NoError(t, reopened.Close())
	require.Equal(t, []alib.Book{books[0], books[1]}, pending)
}

func Test_run_rejects_missing_Alib_tracking_configuration_before_http(t *testing.T) {
	// Given
	useOnceMode(t)
	setEnvironmentAbsentDigestConfiguration(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("TELEGRAM_CHAT_ID", "-100123")
	t.Setenv("TELEGRAM_API_BASE", "http://127.0.0.1:1")

	// When
	err := run(slog.New(slog.DiscardHandler))

	// Then
	require.ErrorIs(t, err, config.ErrInvalid)
	require.ErrorContains(t, err, "ALIB_CATEGORIES")
	require.ErrorContains(t, err, "ALIB_SERIES")
}

func Test_run_sends_only_last_wired_message_with_sound(t *testing.T) {
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
	t.Setenv("ALIB_CATEGORIES", "tramka")
	t.Setenv("ALIB_SERIES", "")
	t.Setenv("TELEGRAM_API_BASE", telegramServer.URL)
	t.Setenv("HTTP_TIMEOUT", "2s")
	t.Setenv("ALIB_REQUEST_INTERVAL", "0s")
	t.Setenv("MESSAGE_LIMIT", "64")
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// When
	err := runWithAlibURLs(t, logger, alibServer.URL+"/tramka.phtml?tnew=7")

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
		if index == len(payloads)-1 {
			require.False(t, payload.DisableNotification)
		} else {
			require.True(t, payload.DisableNotification)
		}
		if index == len(payloads)-1 {
			requireRefreshButton(t, payload)
		} else {
			require.Nil(t, payload.ReplyMarkup)
		}
	}
	require.Contains(t, logs.String(), "digest.completed")
	require.NotContains(t, logs.String(), "test-token")
}

func Test_run_once_fetches_categories_and_series_in_order_and_sends_partial_deduplicated_result(t *testing.T) {
	// Given
	useOnceMode(t)
	alibRequests := make(chan alibRequest, 4)
	alibServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		alibRequests <- alibRequest{Path: request.URL.Path, RawQuery: request.URL.RawQuery}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case request.URL.Path == "/first.phtml":
			_, err := writer.Write([]byte(
				listingPage("Первый", "/first-book.html", "100 руб.") +
					listingPage("Общий первый", "/shared-book.html", "200 руб."),
			))
			assert.NoError(t, err)
		case request.URL.Path == "/broken.phtml":
			writer.WriteHeader(http.StatusBadGateway)
		case request.URL.Path == "/findp.php4" && request.URL.Query().Get("seria") == "Серия, тома":
			_, err := writer.Write([]byte(
				listingPage("Общий второй", "/shared-book.html", "999 руб.") +
					listingPage("Последний", "/last-book.html", "300 руб."),
			))
			assert.NoError(t, err)
		case request.URL.Path == "/findp.php4" && request.URL.Query().Get("seria") == "changed":
			_, err := writer.Write([]byte("<html><body>changed</body></html>"))
			assert.NoError(t, err)
		default:
			t.Errorf("unexpected Alib path %q", request.URL.Path)
		}
	}))
	t.Cleanup(alibServer.Close)

	telegramRequests := make(chan telegramRequest, 2)
	telegramServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		telegramRequests <- telegramRequest{Message: decodeTelegramMessage(t, request), Path: request.URL.Path}
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"ok":true,"result":{}}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(telegramServer.Close)

	statePath := filepath.Join(t.TempDir(), "state.db")
	setRunEnvironment(t, telegramServer.URL, statePath)
	t.Setenv("ALIB_CATEGORIES", "first,broken")
	t.Setenv("ALIB_SERIES", `"Серия, тома",changed`)
	settings, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, []string{
		"https://www.alib.ru/first.phtml?tnew=7",
		"https://www.alib.ru/broken.phtml?tnew=7",
		"https://alib.ru/findp.php4?seria=%D0%A1%D0%B5%D1%80%D0%B8%D1%8F%2C+%D1%82%D0%BE%D0%BC%D0%B0&lday=7",
		"https://alib.ru/findp.php4?seria=changed&lday=7",
	}, settings.AlibURLs)
	settings.AlibURLs = localAlibURLs(t, alibServer.URL, settings.AlibURLs)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// When
	err = runWithConfig(logger, settings, true)

	// Then
	require.NoError(t, err)
	require.Equal(t, []alibRequest{
		{Path: "/first.phtml", RawQuery: "tnew=7"},
		{Path: "/broken.phtml", RawQuery: "tnew=7"},
		{Path: "/findp.php4", RawQuery: "seria=%D0%A1%D0%B5%D1%80%D0%B8%D1%8F%2C+%D1%82%D0%BE%D0%BC%D0%B0&lday=7"},
		{Path: "/findp.php4", RawQuery: "seria=changed&lday=7"},
	}, []alibRequest{<-alibRequests, <-alibRequests, <-alibRequests, <-alibRequests})
	require.Len(t, telegramRequests, 1)
	message := (<-telegramRequests).Message
	richHTML := message.RichMessage.HTML
	for _, buyURL := range []string{
		alibServer.URL + "/first-book.html",
		alibServer.URL + "/shared-book.html",
		alibServer.URL + "/last-book.html",
	} {
		require.Contains(t, richHTML, buyURL)
	}
	require.Less(t, strings.Index(richHTML, "Первый"), strings.Index(richHTML, "Общий первый"))
	require.Less(t, strings.Index(richHTML, "Общий первый"), strings.Index(richHTML, "Последний"))
	require.NotContains(t, richHTML, "Общий второй")
	require.Equal(t, 1, strings.Count(richHTML, "Новые книги на Alib.ru"))
	requireRefreshButton(t, message)
	logOutput := logs.String()
	require.Equal(t, 3, strings.Count(logOutput, `"msg":"alib.page_downloaded"`))
	require.Equal(t, 1, strings.Count(logOutput, `"msg":"alib.page_download_failed"`))
	require.Equal(t, 2, strings.Count(logOutput, `"msg":"alib.page_parsed"`))
	require.Equal(t, 1, strings.Count(logOutput, `"msg":"alib.page_parse_failed"`))
	require.Contains(t, logOutput, `"msg":"alib.page_downloaded","index":0,"url":"`+alibServer.URL+`/first.phtml?tnew=7"`)
	require.Contains(t, logOutput, `"msg":"alib.page_downloaded","index":2,"url":"`+alibServer.URL+`/findp.php4?seria=%D0%A1%D0%B5%D1%80%D0%B8%D1%8F%2C+%D1%82%D0%BE%D0%BC%D0%B0&lday=7"`)
	require.Contains(t, logOutput, `"msg":"alib.page_downloaded","index":3,"url":"`+alibServer.URL+`/findp.php4?seria=changed&lday=7"`)
	require.Contains(t, logOutput, `"msg":"alib.page_download_failed","index":1,"url":"`+alibServer.URL+`/broken.phtml?tnew=7"`)
	require.Contains(t, logOutput, `"msg":"alib.page_parsed","index":0,"url":"`+alibServer.URL+`/first.phtml?tnew=7","books":2`)
	require.Contains(t, logOutput, `"msg":"alib.page_parsed","index":2,"url":"`+alibServer.URL+`/findp.php4?seria=%D0%A1%D0%B5%D1%80%D0%B8%D1%8F%2C+%D1%82%D0%BE%D0%BC%D0%B0&lday=7","books":2`)
	require.Contains(t, logOutput, `"msg":"alib.page_parse_failed","index":3,"url":"`+alibServer.URL+`/findp.php4?seria=changed&lday=7"`)
	require.Less(t,
		strings.LastIndex(logOutput, `"msg":"alib.page_downloaded"`),
		strings.Index(logOutput, `"msg":"alib.page_parsed"`),
	)
	require.NotContains(t, logOutput, `"msg":"alib.page_failed"`)
	require.Contains(t, logOutput, `"msg":"digest.completed"`)
	require.Contains(t, logOutput, `"fetched":3`)
	require.Contains(t, logOutput, `"new":3`)
	require.Contains(t, logOutput, `"sent":3`)

	state, err := store.Open(statePath, time.Now())
	require.NoError(t, err)
	pending, err := state.Pending(context.Background())
	require.NoError(t, err)
	require.NoError(t, state.Close())
	require.Empty(t, pending)
}

func Test_run_once_accepts_all_correct_empty_pages_without_telegram_delivery(t *testing.T) {
	// Given
	useOnceMode(t)
	emptyPage, err := os.ReadFile(filepath.Join("..", "..", "internal", "alib", "testdata", "empty.html"))
	require.NoError(t, err)
	alibRequests := make(chan alibRequest, 2)
	alibServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		alibRequests <- alibRequest{Path: request.URL.Path, RawQuery: request.URL.RawQuery}
		writer.Header().Set("Content-Type", "text/html; charset=windows-1251")
		_, writeErr := writer.Write(emptyPage)
		assert.NoError(t, writeErr)
	}))
	t.Cleanup(alibServer.Close)
	telegramRequests := make(chan struct{}, 1)
	telegramServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		telegramRequests <- struct{}{}
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(telegramServer.Close)

	statePath := filepath.Join(t.TempDir(), "state.db")
	setRunEnvironment(t, telegramServer.URL, statePath)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// When
	err = runWithAlibURLs(t, logger,
		alibServer.URL+"/empty-one?first=true",
		alibServer.URL+"/empty-two?second=true",
	)

	// Then
	require.NoError(t, err)
	require.Equal(t, []alibRequest{
		{Path: "/empty-one", RawQuery: "first=true"},
		{Path: "/empty-two", RawQuery: "second=true"},
	}, []alibRequest{<-alibRequests, <-alibRequests})
	require.Empty(t, telegramRequests)
	logOutput := logs.String()
	require.Equal(t, 2, strings.Count(logOutput, `"msg":"alib.page_downloaded"`))
	require.Equal(t, 2, strings.Count(logOutput, `"msg":"alib.page_parsed"`))
	require.Contains(t, logOutput, `"msg":"alib.page_downloaded","index":0,"url":"`+alibServer.URL+`/empty-one?first=true"`)
	require.Contains(t, logOutput, `"msg":"alib.page_downloaded","index":1,"url":"`+alibServer.URL+`/empty-two?second=true"`)
	require.Contains(t, logOutput, `"msg":"alib.page_parsed","index":0,"url":"`+alibServer.URL+`/empty-one?first=true","books":0`)
	require.Contains(t, logOutput, `"msg":"alib.page_parsed","index":1,"url":"`+alibServer.URL+`/empty-two?second=true","books":0`)
	require.Contains(t, logOutput, `"msg":"digest.completed"`)
	require.Contains(t, logOutput, `"fetched":0`)
	require.Contains(t, logOutput, `"new":0`)
	require.Contains(t, logOutput, `"sent":0`)
	require.NotContains(t, logOutput, "alib.page_failed")
}

func Test_run_once_fails_after_requesting_and_logging_all_failed_pages(t *testing.T) {
	// Given
	useOnceMode(t)
	alibRequests := make(chan string, 3)
	alibServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		alibRequests <- request.URL.Path
		switch request.URL.Path {
		case "/status-one", "/status-two":
			writer.WriteHeader(http.StatusBadGateway)
		case "/broken":
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, err := writer.Write([]byte("<html><body>changed</body></html>"))
			assert.NoError(t, err)
		default:
			t.Errorf("unexpected Alib path %q", request.URL.Path)
		}
	}))
	t.Cleanup(alibServer.Close)
	telegramRequests := make(chan struct{}, 1)
	telegramServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		telegramRequests <- struct{}{}
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(telegramServer.Close)

	statePath := filepath.Join(t.TempDir(), "state.db")
	setRunEnvironment(t, telegramServer.URL, statePath)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// When
	err := runWithAlibURLs(t, logger,
		alibServer.URL+"/status-one?status=one",
		alibServer.URL+"/broken?scope=broken",
		alibServer.URL+"/status-two?status=two",
	)

	// Then
	require.Error(t, err)
	require.Equal(t, []string{"/status-one", "/broken", "/status-two"}, []string{
		<-alibRequests,
		<-alibRequests,
		<-alibRequests,
	})
	require.Empty(t, telegramRequests)
	logOutput := logs.String()
	require.Equal(t, 2, strings.Count(logOutput, `"msg":"alib.page_download_failed"`))
	require.Equal(t, 1, strings.Count(logOutput, `"msg":"alib.page_downloaded"`))
	require.Equal(t, 1, strings.Count(logOutput, `"msg":"alib.page_parse_failed"`))
	require.NotContains(t, logOutput, `"msg":"alib.page_parsed"`)
	require.Contains(t, logOutput, `"msg":"alib.page_download_failed","index":0,"url":"`+
		alibServer.URL+`/status-one?status=one"`)
	require.Contains(t, logOutput, `"msg":"alib.page_downloaded","index":1,"url":"`+
		alibServer.URL+`/broken?scope=broken"`)
	require.Contains(t, logOutput, `"msg":"alib.page_parse_failed","index":1,"url":"`+
		alibServer.URL+`/broken?scope=broken"`)
	require.Contains(t, logOutput, `"msg":"alib.page_download_failed","index":2,"url":"`+
		alibServer.URL+`/status-two?status=two"`)
	require.NotContains(t, logOutput, `"msg":"alib.page_failed"`)
	require.Contains(t, logOutput, `"msg":"digest.failed"`)
}

func useOnceMode(t *testing.T) {
	t.Helper()
	useCommandLine(t, "-once")
}

func runWithAlibURLs(t *testing.T, logger *slog.Logger, endpoints ...string) error {
	t.Helper()
	settings, err := config.Load()
	require.NoError(t, err)
	settings.AlibURLs = append([]string(nil), endpoints...)

	return runWithConfig(logger, settings, true)
}

func localAlibURLs(t *testing.T, base string, endpoints []string) []string {
	t.Helper()
	baseURL, err := url.Parse(base)
	require.NoError(t, err)
	localEndpoints := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		parsed, parseErr := url.Parse(endpoint)
		require.NoError(t, parseErr)
		parsed.Scheme = baseURL.Scheme
		parsed.Host = baseURL.Host
		localEndpoints = append(localEndpoints, parsed.String())
	}

	return localEndpoints
}

func useCommandLine(t *testing.T, arguments ...string) {
	t.Helper()

	originalCommandLine := flag.CommandLine
	originalArgs := os.Args
	t.Cleanup(func() {
		flag.CommandLine = originalCommandLine
		os.Args = originalArgs
	})
	flag.CommandLine = flag.NewFlagSet("alib-fetcher", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	os.Args = append([]string{"alib-fetcher"}, arguments...)
}

func setEnvironmentAbsentDigestConfiguration(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"TELEGRAM_BOT_TOKEN",
		"TELEGRAM_CHAT_ID",
		"CRON_SCHEDULE",
		"TIMEZONE",
		"ALIB_CATEGORIES",
		"ALIB_SERIES",
		"TELEGRAM_API_BASE",
		"HTTP_TIMEOUT",
		"MESSAGE_LIMIT",
		"RUN_ON_STARTUP",
		"FRESH_BOOKS",
		"ALIB_REQUEST_INTERVAL",
	} {
		unsetEnvironment(t, key)
	}
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

func displayedRuneCount(t *testing.T, value string) int {
	t.Helper()
	document, err := xhtml.Parse(strings.NewReader(value))
	require.NoError(t, err)

	var count func(*xhtml.Node) int
	count = func(node *xhtml.Node) int {
		total := 0
		if node.Type == xhtml.TextNode {
			total += len([]rune(node.Data))
		}
		if node.Type == xhtml.ElementNode && node.Data == "br" {
			total++
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			total += count(child)
		}

		return total
	}

	return count(document)
}

func setRunEnvironment(t *testing.T, telegramAPIBase, statePath string) {
	t.Helper()

	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("TELEGRAM_CHAT_ID", "-100123")
	t.Setenv("CRON_SCHEDULE", "0 0 * * *")
	t.Setenv("TIMEZONE", "UTC")
	t.Setenv("RUN_ON_STARTUP", "true")
	t.Setenv("STATE_PATH", statePath)
	t.Setenv("ALIB_CATEGORIES", "tramka")
	t.Setenv("ALIB_SERIES", "")
	t.Setenv("TELEGRAM_API_BASE", telegramAPIBase)
	t.Setenv("HTTP_TIMEOUT", "2s")
	t.Setenv("ALIB_REQUEST_INTERVAL", "0s")
	t.Setenv("MESSAGE_LIMIT", "4000")
}

func unsetEnvironment(t *testing.T, key string) {
	t.Helper()

	t.Setenv(key, "")
	require.NoError(t, os.Unsetenv(key))
}

func listingPage(title, buyURL, price string) string {
	return "<p><b>" + title + "</b> Цена: " + price + " <a href=\"" + buyURL + "\"><b>Купить</b></a></p>"
}
