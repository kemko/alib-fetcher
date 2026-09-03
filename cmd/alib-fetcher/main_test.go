package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	telegrambot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/slink"
	"github.com/kemko/alib-fetcher/internal/store"
	"github.com/kemko/alib-fetcher/internal/telegram"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
				`<br/>Цена: 3 900 руб.<br/>Состояние: Отличное.<br/>Смотрите: <a href="%s/foto.php4?id=1">фото</a>`,
				alibServer.URL,
			))
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

func Test_run_isolatesSlinkPhotoProcessorFailure(t *testing.T) {
	// Given
	useOnceMode(t)
	alibServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err := io.WriteString(writer, `<p><b>Книга.</b> М., 2026 г.<br>
Цена: 100 руб. <a href="/book"><b>Купить</b></a><br>
Смотрите: <a href="http://127.0.0.1/foto.php4">Обложка</a></p>`)
		assert.NoError(t, err)
	}))
	t.Cleanup(alibServer.Close)
	telegramRequests := make(chan telegramRequest, 1)
	telegramServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		telegramRequests <- telegramRequest{Message: decodeTelegramMessage(t, request), Path: request.URL.Path}
		_, err := io.WriteString(writer, `{"ok":true,"result":{}}`)
		assert.NoError(t, err)
	}))
	t.Cleanup(telegramServer.Close)
	setRunEnvironment(t, alibServer.URL, telegramServer.URL, filepath.Join(t.TempDir(), "state.db"))
	t.Setenv("SLINK_URL", "https://slink.example")
	t.Setenv("SLINK_API_KEY", "sk_main-wiring-secret")
	t.Setenv("SLINK_TAG_ID", "550e8400-e29b-41d4-a716-446655440000")
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	// When
	err := run(logger)

	// Then
	require.NoError(t, err)
	request := <-telegramRequests
	require.Contains(t, request.Message.RichMessage.HTML, "Не удалось обработать книг: 1")
	require.Contains(t, logs.String(), `"msg":"slink.photo_failed"`)
	require.NotContains(t, logs.String(), "sk_main-wiring-secret")
}

func Test_run_endToEnd_isolatesSlinkSourceFailure(t *testing.T) {
	// Given
	useOnceMode(t)
	const (
		apiKey = "sk_acceptance-secret"
		tagID  = "550e8400-e29b-41d4-a716-446655440000"
	)

	alibServer := newIPv4TestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err := fmt.Fprintf(writer, `<p><b>Успешная.</b> М., 2026 г.<br>
Цена: 100 руб. <a href="/good-buy"><b>Купить</b></a><br>
Смотрите: <a href="http://slink.test/foto.php4?kind=good">Обложка</a></p>
<p><b>Сбойная.</b> М., 2026 г.<br>
Цена: 200 руб. <a href="/bad-buy"><b>Купить</b></a><br>
Смотрите: <a href="http://slink.test/foto.php4?kind=bad">Обложка</a></p>`)
		assert.NoError(t, err)
	}))
	t.Cleanup(alibServer.Close)

	slinkServer := newIPv4TestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/foto.php4":
			if request.URL.Query().Get("kind") == "bad" {
				writer.WriteHeader(http.StatusForbidden)
				return
			}
			assert.Equal(t, http.MethodGet, request.Method)
			assert.Equal(t, "alib-fetcher/1.0", request.Header.Get("User-Agent"))
			assert.Equal(t, alibServer.URL+"/good-buy", request.Header.Get("Referer"))
			writer.Header().Set("Content-Type", "image/png")
			_, err := writer.Write([]byte("\x89PNG\r\n\x1a\nimage"))
			assert.NoError(t, err)
		case "/bad-photo":
			writer.WriteHeader(http.StatusForbidden)
		case "/api/external/upload":
			assert.Equal(t, http.MethodPost, request.Method)
			assert.Equal(t, "Bearer "+apiKey, request.Header.Get("Authorization"))
			assert.Equal(t, "http://slink.test", request.Header.Get("Origin"))
			assert.Equal(t, "alib-fetcher/1.0", request.Header.Get("User-Agent"))
			if !assert.NoError(t, request.ParseMultipartForm(1<<20)) {
				return
			}
			file, _, err := request.FormFile("image")
			if !assert.NoError(t, err) {
				return
			}
			assert.NoError(t, file.Close())
			assert.Equal(t, tagID, request.FormValue("tagIds[]"))
			writer.Header().Set("Content-Type", "application/json")
			_, err = io.WriteString(writer, `{"url":"/published/good.png"}`)
			assert.NoError(t, err)
		case "/published/good.png":
			assert.Equal(t, http.MethodHead, request.Method)
			writer.Header().Set("Content-Type", "image/png")
		default:
			t.Errorf("unexpected Slink path %q", request.URL.Path)
		}
	}))
	t.Cleanup(slinkServer.Close)

	telegramRequests := make(chan telegramRequest, 1)
	telegramServer := newIPv4TestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		telegramRequests <- telegramRequest{Message: decodeTelegramMessage(t, request), Path: request.URL.Path}
		_, err := io.WriteString(writer, `{"ok":true,"result":{}}`)
		assert.NoError(t, err)
	}))
	t.Cleanup(telegramServer.Close)

	statePath := filepath.Join(t.TempDir(), "state.db")
	setRunEnvironment(t, alibServer.URL, telegramServer.URL, statePath)
	t.Setenv("SLINK_URL", "http://slink.test")
	t.Setenv("SLINK_API_KEY", apiKey)
	t.Setenv("SLINK_TAG_ID", tagID)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	newSlinkClient := func(rawURL, key, tag string, timeout time.Duration, clientLogger *slog.Logger) (*slink.Client, error) {
		dialer := &net.Dialer{}
		return slink.NewClientWithOptions(rawURL, key, tag, timeout, clientLogger, slink.Options{
			HTTPClient: &http.Client{Transport: rewriteHostTransport{target: slinkServer.Listener.Addr().String()}},
			LookupIP: func(context.Context, string) ([]net.IP, error) {
				return []net.IP{net.ParseIP("93.184.216.34")}, nil
			},
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, slinkServer.Listener.Addr().String())
			},
		})
	}

	// When
	err := runWithSlinkFactory(logger, newSlinkClient)

	// Then
	require.NoError(t, err)
	require.Len(t, telegramRequests, 1)
	message := (<-telegramRequests).Message
	require.Contains(t, message.RichMessage.HTML, "Успешная.")
	require.Contains(t, message.RichMessage.HTML, "http://slink.test/published/good.png")
	require.NotContains(t, message.RichMessage.HTML, "Сбойная.")
	require.Contains(t, message.RichMessage.HTML, "Не удалось обработать книг: 1")
	requireRefreshButton(t, message)

	state, err := store.Open(statePath, time.Now())
	require.NoError(t, err)
	existing, err := state.Existing(context.Background(), []alib.Book{
		{BuyURL: alibServer.URL + "/good-buy"},
		{BuyURL: alibServer.URL + "/bad-buy"},
	})
	require.NoError(t, err)
	require.Equal(t, []bool{true, false}, existing)
	pending, err := state.Pending(context.Background())
	require.NoError(t, err)
	require.Empty(t, pending)
	require.NoError(t, state.Close())

	logOutput := logs.String()
	require.Contains(t, logOutput, `"msg":"slink.photo_started","buy_url":"`+alibServer.URL+`/good-buy","index":0,"total":1`)
	require.Contains(t, logOutput, `"msg":"slink.photo_completed","buy_url":"`+alibServer.URL+`/good-buy","index":0,"total":1,"outcome":"uploaded","media_url":"http://slink.test/published/good.png"`)
	require.Contains(t, logOutput, `"msg":"slink.photo_failed"`)
	require.Contains(t, logOutput, `"buy_url":"`+alibServer.URL+`/bad-buy","index":0,"total":1`)
	require.Contains(t, logOutput, `"stage":"source_download"`)
	require.Contains(t, logOutput, `"http_status":403`)
	require.Contains(t, logOutput, `"msg":"digest.completed"`)
	require.Contains(t, logOutput, `"failed":1`)
	require.NotContains(t, logOutput, apiKey)
	require.NotContains(t, logOutput, "kind=good")
	require.NotContains(t, logOutput, "kind=bad")
}

type rewriteHostTransport struct {
	target string
}

func (transport rewriteHostTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	rewritten := request.Clone(request.Context())
	rewritten.URL.Host = transport.target

	response, err := http.DefaultTransport.RoundTrip(rewritten)
	if response != nil && response.Request != nil {
		response.Request = request
	}

	return response, err
}

func newIPv4TestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(context.Background(), "tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()

	return server
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
	// These endpoints are intentionally unreachable: maintenance mode must not
	// construct the Alib or Telegram adapters that would use them.
	t.Setenv("ALIB_URL", "http://127.0.0.1:1")
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
	t.Setenv("ALIB_URL", alibServer.URL+"/tramka.phtml?tnew=7")
	t.Setenv("TELEGRAM_API_BASE", telegramServer.URL)
	t.Setenv("HTTP_TIMEOUT", "2s")
	t.Setenv("ALIB_REQUEST_INTERVAL", "0s")
	t.Setenv("MESSAGE_LIMIT", "64")
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

func Test_run_once_fetches_multiple_urls_and_sends_partial_deduplicated_result(t *testing.T) {
	// Given
	useOnceMode(t)
	alibRequests := make(chan alibRequest, 4)
	alibServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		alibRequests <- alibRequest{Path: request.URL.Path, RawQuery: request.URL.RawQuery}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch request.URL.Path {
		case "/first":
			_, err := writer.Write([]byte(
				listingPage("Первый", "/first-book.html", "100 руб.") +
					listingPage("Общий первый", "/shared-book.html", "200 руб."),
			))
			assert.NoError(t, err)
		case "/broken":
			writer.WriteHeader(http.StatusBadGateway)
		case "/second":
			_, err := writer.Write([]byte(
				listingPage("Общий второй", "/shared-book.html", "999 руб.") +
					listingPage("Последний", "/last-book.html", "300 руб."),
			))
			assert.NoError(t, err)
		case "/changed":
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
	setRunEnvironment(t, alibServer.URL, telegramServer.URL, statePath)
	t.Setenv("ALIB_URL", strings.Join([]string{
		alibServer.URL + "/first?scope=one&format=full",
		alibServer.URL + "/broken?scope=two",
		alibServer.URL + "/second?topic=one%2Ctwo&format=full",
		alibServer.URL + "/changed?scope=broken",
	}, ", "))
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// When
	err := run(logger)

	// Then
	require.NoError(t, err)
	require.Equal(t, []alibRequest{
		{Path: "/first", RawQuery: "scope=one&format=full"},
		{Path: "/broken", RawQuery: "scope=two"},
		{Path: "/second", RawQuery: "topic=one%2Ctwo&format=full"},
		{Path: "/changed", RawQuery: "scope=broken"},
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
	require.Contains(t, logOutput, `"msg":"alib.page_downloaded","index":0,"url":"`+alibServer.URL+`/first?scope=one&format=full"`)
	require.Contains(t, logOutput, `"msg":"alib.page_downloaded","index":2,"url":"`+alibServer.URL+`/second?topic=one%2Ctwo&format=full"`)
	require.Contains(t, logOutput, `"msg":"alib.page_downloaded","index":3,"url":"`+alibServer.URL+`/changed?scope=broken"`)
	require.Contains(t, logOutput, `"msg":"alib.page_download_failed","index":1,"url":"`+alibServer.URL+`/broken?scope=two"`)
	require.Contains(t, logOutput, `"msg":"alib.page_parsed","index":0,"url":"`+alibServer.URL+`/first?scope=one&format=full","books":2`)
	require.Contains(t, logOutput, `"msg":"alib.page_parsed","index":2,"url":"`+alibServer.URL+`/second?topic=one%2Ctwo&format=full","books":2`)
	require.Contains(t, logOutput, `"msg":"alib.page_parse_failed","index":3,"url":"`+alibServer.URL+`/changed?scope=broken"`)
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
	setRunEnvironment(t, alibServer.URL, telegramServer.URL, statePath)
	t.Setenv("ALIB_URL", alibServer.URL+"/empty-one?first=true, "+alibServer.URL+"/empty-two?second=true")
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// When
	err = run(logger)

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
	setRunEnvironment(t, alibServer.URL, telegramServer.URL, statePath)
	t.Setenv("ALIB_URL", strings.Join([]string{
		alibServer.URL + "/status-one?status=one",
		alibServer.URL + "/broken?scope=broken",
		alibServer.URL + "/status-two?status=two",
	}, ","))
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// When
	err := run(logger)

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
		"ALIB_URL",
		"TELEGRAM_API_BASE",
		"HTTP_TIMEOUT",
		"MESSAGE_LIMIT",
		"RUN_ON_STARTUP",
		"FRESH_BOOKS",
		"ALIB_REQUEST_INTERVAL",
		"SLINK_URL",
		"SLINK_API_KEY",
		"SLINK_TAG_ID",
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
	t.Setenv("ALIB_REQUEST_INTERVAL", "0s")
	t.Setenv("MESSAGE_LIMIT", "4000")
	t.Setenv("SLINK_URL", "")
	t.Setenv("SLINK_API_KEY", "")
	t.Setenv("SLINK_TAG_ID", "")
}

func unsetEnvironment(t *testing.T, key string) {
	t.Helper()

	t.Setenv(key, "")
	require.NoError(t, os.Unsetenv(key))
}

func listingPage(title, buyURL, price string) string {
	return "<p><b>" + title + "</b> Цена: " + price + " <a href=\"" + buyURL + "\"><b>Купить</b></a></p>"
}
