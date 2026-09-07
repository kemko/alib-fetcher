package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	telegrambot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/config"
	"github.com/kemko/alib-fetcher/internal/process"
	"github.com/kemko/alib-fetcher/internal/store"
	"github.com/kemko/alib-fetcher/internal/telegram"
	"github.com/kemko/alib-fetcher/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/text/encoding/charmap"
)

type telegramRequest struct {
	Message telegrambot.SendRichMessageParams
	Path    string
}

type alibRequest struct {
	Path     string
	RawQuery string
}

type reloadAlibRequest struct {
	At   time.Time
	Path string
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
			routeAlibRequestsTo(t, alibServer.URL)

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
				"https://www.alib.ru",
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
				"https://www.alib.ru",
				"https://www.alib.ru",
				"https://www.alib.ru",
			))
			require.NotContains(t, richHTML, "<tg-slideshow>")
			require.NotContains(t, richHTML, "<img ")
			require.NotContains(t, richHTML, "<p>")
			require.NotContains(t, richHTML, "<br>")
			require.NotRegexp(t, `[\r\n]`, richHTML)
			require.True(
				t,
				strings.HasSuffix(richHTML, `<a href="https://www.alib.ru/fresh.html">Купить</a>`),
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
		require.LessOrEqual(t, testutil.DisplayedRuneCount(t, request.Message.RichMessage.HTML), messageLimit)
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
		require.LessOrEqual(t, testutil.DisplayedRuneCount(t, request.Message.RichMessage.HTML), 32000)
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

func Test_parseCommandLine_accepts_each_mode(t *testing.T) {
	t.Parallel()

	testCases := map[string]struct {
		arguments []string
		want      commandOptions
	}{
		"once for every chat": {
			arguments: []string{"alib-fetcher", "-once", "-config", "./config.toml"},
			want: commandOptions{
				configPath: "./config.toml",
				once:       true,
			},
		},
		"once for selected chat": {
			arguments: []string{
				"alib-fetcher", "-once", "-chat=-1001234567890", "-config", "./config.toml",
			},
			want: commandOptions{
				configPath: "./config.toml",
				chatID:     "-1001234567890",
				once:       true,
			},
		},
		"service": {
			arguments: []string{"alib-fetcher", "-service", "-config", "./config.toml"},
			want: commandOptions{
				configPath: "./config.toml",
				service:    true,
			},
		},
		"once with inactive service and help": {
			arguments: []string{"alib-fetcher", "-once", "-service=false", "--help=false"},
			want: commandOptions{
				configPath: "./config.toml",
				once:       true,
			},
		},
		"service with inactive once": {
			arguments: []string{"alib-fetcher", "-service", "-once=false"},
			want: commandOptions{
				configPath: "./config.toml",
				service:    true,
			},
		},
		"decimal forget latest with inactive modes": {
			arguments: []string{
				"alib-fetcher", "-forget-latest=010", "-chat", "-100123", "-once=false", "-service=false",
			},
			want: commandOptions{
				configPath: "./config.toml",
				chatID:     "-100123",
				forgetLatest: forgetLatestOption{
					value: 10,
					set:   true,
				},
			},
		},
		"forget latest": {
			arguments: []string{
				"alib-fetcher", "-forget-latest=7", "--chat", "@Books", "--config", "settings.toml",
			},
			want: commandOptions{
				configPath: "settings.toml",
				chatID:     "@books",
				forgetLatest: forgetLatestOption{
					value: 7,
					set:   true,
				},
			},
		},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			var output, errors bytes.Buffer

			got, err := parseCommandLineArgs(testCase.arguments, &output, &errors)

			require.NoError(t, err)
			require.Equal(t, testCase.want, got)
			require.Empty(t, output.String())
			require.Empty(t, errors.String())
		})
	}
}

func Test_parseCommandLine_rejects_invalid_arguments(t *testing.T) {
	t.Parallel()

	testCases := map[string]struct {
		wantError string
		arguments []string
	}{
		"missing mode": {
			arguments: []string{"alib-fetcher", "-config", "settings.toml"},
			wantError: "exactly one of",
		},
		"conflicting modes": {
			arguments: []string{"alib-fetcher", "-once", "-service"},
			wantError: "exactly one of",
		},
		"conflicting modes with inactive help": {
			arguments: []string{"alib-fetcher", "--help=false", "-once", "-service"},
			wantError: "exactly one of",
		},
		"inactive once": {
			arguments: []string{"alib-fetcher", "-once=false"},
			wantError: "exactly one of",
		},
		"inactive service": {
			arguments: []string{"alib-fetcher", "-service=false"},
			wantError: "exactly one of",
		},
		"forget latest without chat": {
			arguments: []string{"alib-fetcher", "-forget-latest", "1"},
			wantError: "-chat is required with -forget-latest",
		},
		"service with chat": {
			arguments: []string{"alib-fetcher", "-service", "-chat=-100123"},
			wantError: "-chat is incompatible with -service",
		},
		"unknown flag": {
			arguments: []string{"alib-fetcher", "-once", "-unknown"},
			wantError: "flag provided but not defined",
		},
		"unknown flag with inactive help": {
			arguments: []string{"alib-fetcher", "--help=false", "-unknown"},
			wantError: "flag provided but not defined",
		},
		"positional argument": {
			arguments: []string{"alib-fetcher", "-once", "unexpected"},
			wantError: "positional arguments",
		},
		"missing config value": {
			arguments: []string{"alib-fetcher", "-once", "-config"},
			wantError: "flag needs an argument",
		},
		"missing chat value": {
			arguments: []string{"alib-fetcher", "-once", "-chat"},
			wantError: "flag needs an argument",
		},
		"missing forget latest value": {
			arguments: []string{"alib-fetcher", "-forget-latest"},
			wantError: "flag needs an argument",
		},
		"invalid bool": {
			arguments: []string{"alib-fetcher", "-once=maybe"},
			wantError: "invalid value",
		},
		"zero forget latest": {
			arguments: []string{"alib-fetcher", "-forget-latest=0", "-chat", "-100123"},
			wantError: "-forget-latest must be positive",
		},
		"negative forget latest": {
			arguments: []string{"alib-fetcher", "-forget-latest", "-1", "-chat", "-100123"},
			wantError: "-forget-latest must be positive",
		},
		"overflowing forget latest": {
			arguments: []string{"alib-fetcher", "-forget-latest", strings.Repeat("9", 100)},
			wantError: "invalid value",
		},
		"non-numeric forget latest": {
			arguments: []string{"alib-fetcher", "-forget-latest", "six"},
			wantError: "invalid value",
		},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			var output, errors bytes.Buffer

			_, err := parseCommandLineArgs(testCase.arguments, &output, &errors)

			var commandErr commandError
			require.ErrorAs(t, err, &commandErr)
			require.ErrorContains(t, err, testCase.wantError)
			require.Contains(t, output.String()+errors.String(), "USAGE:")
		})
	}
}

func Test_parseCommandLine_normalizes_chat_and_tracks_explicit_values(t *testing.T) {
	t.Parallel()

	testCases := map[string]struct {
		arguments []string
		want      commandOptions
	}{
		"negative chat with separate value": {
			arguments: []string{"alib-fetcher", "-once", "-chat", "-100123"},
			want: commandOptions{
				configPath: "./config.toml",
				once:       true,
				chatID:     "-100123",
			},
		},
		"negative chat with equals": {
			arguments: []string{"alib-fetcher", "--once", "--chat=-100123"},
			want: commandOptions{
				configPath: "./config.toml",
				once:       true,
				chatID:     "-100123",
			},
		},
		"channel is normalized": {
			arguments: []string{"alib-fetcher", "-once", "-chat", "@Books"},
			want: commandOptions{
				configPath: "./config.toml",
				once:       true,
				chatID:     "@books",
			},
		},
		"forget latest is explicitly set": {
			arguments: []string{"alib-fetcher", "-forget-latest", "7", "-chat", "@BOOKS"},
			want: commandOptions{
				configPath: "./config.toml",
				chatID:     "@books",
				forgetLatest: forgetLatestOption{
					value: 7,
					set:   true,
				},
			},
		},
		"omitted values stay unset": {
			arguments: []string{"alib-fetcher", "-once"},
			want: commandOptions{
				configPath: "./config.toml",
				once:       true,
			},
		},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			var output, errors bytes.Buffer

			got, err := parseCommandLineArgs(testCase.arguments, &output, &errors)

			require.NoError(t, err)
			require.Equal(t, testCase.want, got)
			require.Empty(t, output.String())
			require.Empty(t, errors.String())
		})
	}
}

func Test_parseCommandLine_rejects_empty_or_invalid_chat(t *testing.T) {
	t.Parallel()

	for name, arguments := range map[string][]string{
		"empty with equals":         {"alib-fetcher", "-once", "-chat="},
		"empty with separate value": {"alib-fetcher", "-once", "-chat", ""},
		"invalid value":             {"alib-fetcher", "-once", "-chat", "books"},
	} {
		t.Run(name, func(t *testing.T) {
			var output, errors bytes.Buffer

			_, err := parseCommandLineArgs(arguments, &output, &errors)

			var commandErr commandError
			require.ErrorAs(t, err, &commandErr)
			require.Contains(t, output.String()+errors.String(), "USAGE:")
		})
	}
}

func Test_parseCommandLine_help_is_available_without_configuration(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{
		{"alib-fetcher"},
		{"alib-fetcher", "-h"},
		{"alib-fetcher", "-help"},
		{"alib-fetcher", "--help"},
	} {
		t.Run(strings.Join(arguments[1:], "_"), func(t *testing.T) {
			var output, errors bytes.Buffer

			options, err := parseCommandLineArgs(arguments, &output, &errors)

			require.NoError(t, err)
			require.True(t, options.help)
			require.Contains(t, output.String(), "USAGE:")
			require.Contains(t, output.String(), "-once")
			require.Contains(t, output.String(), "-forget-latest")
			require.Empty(t, errors.String())
		})
	}
}

func Test_main_subprocess_exit_codes(t *testing.T) {
	t.Parallel()

	missingConfig := filepath.Join(t.TempDir(), "missing.toml")
	testCases := map[string]struct {
		arguments []string
		wantCode  int
	}{
		"help": {
			arguments: []string{"-h"},
			wantCode:  0,
		},
		"argument error": {
			arguments: []string{"-unknown"},
			wantCode:  2,
		},
		"argument error with inactive help": {
			arguments: []string{"--help=false", "-unknown"},
			wantCode:  2,
		},
		"conflicting modes with inactive help": {
			arguments: []string{"--help=false", "-once", "-service"},
			wantCode:  2,
		},
		"configuration error": {
			arguments: []string{"-once", "-config", missingConfig},
			wantCode:  1,
		},
		"configuration error with inactive help": {
			arguments: []string{"-once", "--help=false", "-config", missingConfig},
			wantCode:  1,
		},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			encodedArguments, err := json.Marshal(testCase.arguments)
			require.NoError(t, err)
			command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestMainSubprocess$", "-test.v=false")
			command.Env = append(os.Environ(),
				"ALIB_FETCHER_MAIN_SUBPROCESS=1",
				"ALIB_FETCHER_MAIN_ARGS="+string(encodedArguments),
			)

			output, err := command.CombinedOutput()

			var exitErr *exec.ExitError
			if testCase.wantCode == 0 {
				require.NoError(t, err, string(output))
			} else {
				require.ErrorAs(t, err, &exitErr)
				require.Equal(t, testCase.wantCode, exitErr.ExitCode(), string(output))
			}
		})
	}
}

func TestMainSubprocess(t *testing.T) {
	if os.Getenv("ALIB_FETCHER_MAIN_SUBPROCESS") != "1" {
		return
	}

	var arguments []string
	require.NoError(t, json.Unmarshal([]byte(os.Getenv("ALIB_FETCHER_MAIN_ARGS")), &arguments))
	os.Args = append([]string{"alib-fetcher"}, arguments...)
	main()
}

func Test_forgetLatestOption_rejects_malformed_values(t *testing.T) {
	testCases := map[string]struct {
		wantError string
		arguments []string
	}{
		"non-numeric": {
			arguments: []string{"-forget-latest", "six"},
			wantError: "invalid value",
		},
		"overflowing": {
			arguments: []string{"-forget-latest", strings.Repeat("9", 100)},
			wantError: "invalid value",
		},
		"missing": {
			arguments: []string{"-forget-latest"},
			wantError: "flag needs an argument",
		},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			// Given
			useCommandLine(t, testCase.arguments...)

			// When
			_, err := parseCommandLine()

			// Then
			require.ErrorContains(t, err, testCase.wantError)
		})
	}
}

func Test_run_rejects_forget_latest_with_once(t *testing.T) {
	// Given
	useCommandLine(t, "-once", "-forget-latest", "1")

	// When
	err := run(slog.New(slog.DiscardHandler))

	// Then
	require.ErrorContains(t, err, "exactly one of")
}

func Test_run_without_arguments_only_prints_help(t *testing.T) {
	useCommandLine(t)

	require.NoError(t, run(slog.New(slog.DiscardHandler)))
}

func Test_run_help_does_not_read_configuration(t *testing.T) {
	useCommandLine(t, "-help", "-config", filepath.Join(t.TempDir(), "missing.toml"))

	require.NoError(t, run(slog.New(slog.DiscardHandler)))
}

func Test_run_rejects_positional_arguments_before_configuration(t *testing.T) {
	useCommandLine(t, "-once", "unexpected")

	err := run(slog.New(slog.DiscardHandler))

	require.ErrorContains(t, err, "positional arguments")
}

func Test_run_rejects_unknown_chat_before_creating_adapters(t *testing.T) {
	configPath := writeMainConfig(t, `[[chats]]
chat_id = "-100"
telegram_token = "secret"
categories = ["tramka"]
`)
	useCommandLine(t, "-once", "-chat", "-101", "-config", configPath)

	err := run(slog.New(slog.DiscardHandler))

	require.ErrorContains(t, err, `unknown chat "-101"`)
}

func Test_run_once_isolates_recipients_and_retries_only_failed_delivery(t *testing.T) {
	// Given
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	config := fmt.Sprintf(`state_path = %q
http_timeout = "2s"
alib_max_retries = 0

[[chats]]
chat_id = "-1001"
telegram_token = "old-token"
state_file = "first.db"
categories = ["tramka"]

[chats.filters]
seria = ["Первый фильтр"]

[[chats]]
chat_id = "-1002"
telegram_token = "new-token"
state_file = "second.db"
categories = ["detektivy"]

[chats.filters]
title = ["Второй фильтр"]
`, root)
	require.NoError(t, os.WriteFile(configPath, []byte(config), 0o600))

	var alibPaths atomic.Int32
	alibServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		alibPaths.Add(1)
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err := fmt.Fprint(writer, testutil.ListingPage("Общая книга", "/shared.html", "100 руб."))
		assert.NoError(t, err)
	}))
	t.Cleanup(alibServer.Close)
	routeAlibRequestsTo(t, alibServer.URL)

	var oldSends atomic.Int32
	var newSends atomic.Int32
	telegramServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/botold-token/sendRichMessage":
			if oldSends.Add(1) == 1 {
				writer.WriteHeader(http.StatusBadGateway)
				_, err := io.WriteString(writer, `{"ok":false,"error_code":502,"description":"temporary failure"}`)
				assert.NoError(t, err)

				return
			}
		case "/botnew-token/sendRichMessage":
			newSends.Add(1)
		default:
			t.Fatalf("unexpected Telegram request path %q", request.URL.Path)
		}
		writeTelegramResponse(t, writer, `{"ok":true,"result":{}}`)
	}))
	t.Cleanup(telegramServer.Close)
	routeTelegramRequestsTo(t, telegramServer.URL)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	// When
	useCommandLine(t, "-once", "-config", configPath)
	firstErr := run(logger)
	useCommandLine(t, "-once", "-config", configPath)
	secondErr := run(logger)

	// Then
	require.Error(t, firstErr)
	require.NoError(t, secondErr)
	require.Equal(t, int32(8), alibPaths.Load())
	require.Equal(t, int32(2), oldSends.Load())
	require.Equal(t, int32(2), newSends.Load())

	firstState, err := store.Open(filepath.Join(root, "first.db"), time.Now())
	require.NoError(t, err)
	firstPending, err := firstState.Pending(context.Background())
	require.NoError(t, err)
	require.NoError(t, firstState.Close())
	require.Empty(t, firstPending)
	secondState, err := store.Open(filepath.Join(root, "second.db"), time.Now())
	require.NoError(t, err)
	secondPending, err := secondState.Pending(context.Background())
	require.NoError(t, err)
	require.NoError(t, secondState.Close())
	require.Empty(t, secondPending)
	loggedChats := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var event struct {
			Message string `json:"msg"`
			ChatID  string `json:"chat_id"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &event))
		if strings.HasPrefix(event.Message, "alib.page_") {
			require.Contains(t, []string{"-1001", "-1002"}, event.ChatID)
			loggedChats[event.ChatID] = true
		}
	}
	require.Len(t, loggedChats, 2)
}

func Test_run_once_all_and_selected_chat_keep_other_state_untouched(t *testing.T) {
	for _, selected := range []bool{false, true} {
		name := "all"
		if selected {
			name = "selected"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			configPath := filepath.Join(root, "config.toml")
			content := fmt.Sprintf(`state_path = %q
http_timeout = "2s"
alib_max_retries = 0

[[chats]]
chat_id = "-1001"
telegram_token = "first-token"
categories = ["tramka"]

[[chats]]
chat_id = "-1002"
telegram_token = "second-token"
state_file = "archive.db"
categories = ["detektivy"]
`, root)
			require.NoError(t, os.WriteFile(configPath, []byte(content), 0o600))

			var alibPaths []string
			var pathsMu sync.Mutex
			alibServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				pathsMu.Lock()
				alibPaths = append(alibPaths, request.URL.Path)
				pathsMu.Unlock()
				writer.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, err := fmt.Fprint(writer, testutil.ListingPage("Книга", "/book.html", "100 руб."))
				assert.NoError(t, err)
			}))
			t.Cleanup(alibServer.Close)
			routeAlibRequestsTo(t, alibServer.URL)
			telegramChats := make(chan string, 4)
			telegramServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if strings.HasSuffix(request.URL.Path, "/sendRichMessage") {
					telegramChats <- request.FormValue("chat_id")
				}
				writeTelegramResponse(t, writer, `{"ok":true,"result":{}}`)
			}))
			t.Cleanup(telegramServer.Close)
			routeTelegramRequestsTo(t, telegramServer.URL)

			archivePath := filepath.Join(root, "archive.db")
			archive, err := store.Open(archivePath, time.Now())
			require.NoError(t, err)
			untouched := alib.Book{BuyURL: "https://example.com/untouched"}
			_, err = archive.RecordDiscovered(context.Background(), []alib.Book{untouched}, time.Now())
			require.NoError(t, err)
			require.NoError(t, archive.Close())
			before, err := os.ReadFile(archivePath)
			require.NoError(t, err)

			args := []string{"-once", "-config", configPath}
			if selected {
				args = append(args, "-chat", "-1001")
			}
			useCommandLine(t, args...)

			// When
			require.NoError(t, run(slog.New(slog.DiscardHandler)))

			// Then
			pathsMu.Lock()
			gotPaths := append([]string(nil), alibPaths...)
			pathsMu.Unlock()
			if selected {
				require.Equal(t, []string{"/tramka.phtml"}, gotPaths)
				require.Equal(t, "-1001", <-telegramChats)
			} else {
				require.ElementsMatch(t, []string{"/tramka.phtml", "/detektivy.phtml"}, gotPaths)
				require.ElementsMatch(t, []string{"-1001", "-1002"}, []string{<-telegramChats, <-telegramChats})
			}
			require.FileExists(t, filepath.Join(root, "-1001.db"))
			require.FileExists(t, archivePath)
			if selected {
				require.NoFileExists(t, filepath.Join(root, "-1002.db"))
			}
			after, err := os.ReadFile(archivePath)
			require.NoError(t, err)
			if selected {
				require.Equal(t, before, after)
			}
		})
	}
}

func Test_run_forget_latest_uses_legacy_state_file_without_http_or_service_settings(t *testing.T) {
	// Given
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	statePath := filepath.Join(root, "old-state.db")
	content := fmt.Sprintf("state_path = %q\n\n[[chats]]\nchat_id = \"@Books\"\nstate_file = \"old-state.db\"\n", root)
	require.NoError(t, os.WriteFile(configPath, []byte(content), 0o600))
	state, err := store.Open(statePath, time.Now())
	require.NoError(t, err)
	_, err = state.RecordDiscovered(context.Background(), []alib.Book{{BuyURL: "https://example.com/old"}}, time.Now())
	require.NoError(t, err)
	require.NoError(t, state.Close())
	before, err := os.ReadFile(statePath)
	require.NoError(t, err)
	useCommandLine(t, "-forget-latest", "1", "-chat", "@books", "-config", configPath)

	// When
	err = run(slog.New(slog.DiscardHandler))

	// Then
	require.NoError(t, err)
	after, err := os.ReadFile(statePath)
	require.NoError(t, err)
	require.NotEqual(t, before, after)
}

func Test_run_rejects_cli_syntax_before_reading_config_or_opening_state(t *testing.T) {
	for _, arguments := range [][]string{
		{"-once", "-config"},
		{"-unknown"},
		{"-once", "unexpected"},
		{"-once", "-chat="},
		{"-once", "-chat", ""},
		{"-service", "-chat="},
		{"-forget-latest", "1", "-chat="},
	} {
		t.Run(strings.Join(arguments, "_"), func(t *testing.T) {
			missingConfig := filepath.Join(t.TempDir(), "missing.toml")
			useCommandLine(t, append(arguments, "-config", missingConfig)...)
			err := run(slog.New(slog.DiscardHandler))
			var argumentErr commandError
			require.ErrorAs(t, err, &argumentErr)
			require.NoFileExists(t, missingConfig)
		})
	}
}

func Test_service_reload_applies_new_search_token_and_state_after_inflight_send(t *testing.T) {
	// Given
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	initialConfig := fmt.Sprintf(`state_path = %q
cron_schedule = "@every 1s"
timezone = "UTC"
run_on_startup = true
http_timeout = "10s"
alib_download_delay = "0s"
alib_max_retries = 0

[[chats]]
chat_id = "-1001"
telegram_token = "old-token"
state_file = "old.db"
categories = ["tramka"]
`, root)
	updatedConfig := fmt.Sprintf(`state_path = %q
cron_schedule = "@every 1s"
timezone = "UTC"
run_on_startup = true
http_timeout = "10s"
alib_download_delay = "100ms"
alib_max_retries = 0

[[chats]]
chat_id = "-1001"
telegram_token = "new-token"
state_file = "new.db"
categories = ["detektivy", "poisk"]
`, root)
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0o600))

	oldSendStarted := make(chan struct{})
	releaseOldSend := make(chan struct{})
	newSendStarted := make(chan struct{})
	var alibPathsMu sync.Mutex
	var alibRequests []reloadAlibRequest
	alibServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		alibPathsMu.Lock()
		alibRequests = append(alibRequests, reloadAlibRequest{At: time.Now(), Path: request.URL.Path})
		alibPathsMu.Unlock()
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err := fmt.Fprint(writer, testutil.ListingPage("Книга", "/book.html", "100 руб."))
		assert.NoError(t, err)
	}))
	t.Cleanup(alibServer.Close)
	routeAlibRequestsTo(t, alibServer.URL)
	telegramServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/botold-token/sendRichMessage":
			select {
			case <-oldSendStarted:
			default:
				close(oldSendStarted)
			}
			select {
			case <-releaseOldSend:
			case <-request.Context().Done():
				return
			}
		case "/botnew-token/sendRichMessage":
			select {
			case <-newSendStarted:
			default:
				close(newSendStarted)
			}
		case "/botold-token/getUpdates", "/botnew-token/getUpdates":
			writeTelegramResponse(t, writer, `{"ok":true,"result":[]}`)

			return
		default:
			t.Fatalf("unexpected Telegram request path %q", request.URL.Path)
		}
		writeTelegramResponse(t, writer, `{"ok":true,"result":{}}`)
	}))
	t.Cleanup(telegramServer.Close)
	routeTelegramRequestsTo(t, telegramServer.URL)

	reloadPending := make(chan struct{})
	logger := slog.New(slog.NewJSONHandler(&reloadLogSignal{pending: reloadPending}, nil))
	settings, err := config.Load(configPath)
	require.NoError(t, err)
	factory := newRuntimeFactory(logger)
	initial, err := factory.snapshot(settings, "")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- process.RunReloadable(ctx, initial, func(loadCtx context.Context) (process.ReloadSnapshot, error) {
			latest, loadErr := config.Load(configPath)
			if loadErr != nil {
				return process.ReloadSnapshot{}, loadErr
			}

			return factory.snapshot(latest, "")
		}, logger)
	}()
	select {
	case <-oldSendStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("initial digest did not reach Telegram")
	}

	// When
	temporaryConfig := configPath + ".tmp"
	require.NoError(t, os.WriteFile(temporaryConfig, []byte(updatedConfig), 0o600))
	require.NoError(t, os.Rename(temporaryConfig, configPath))
	select {
	case <-reloadPending:
	case <-time.After(5 * time.Second):
		t.Fatal("reload did not pause new digests")
	}
	require.NoFileExists(t, filepath.Join(root, "new.db"))
	close(releaseOldSend)
	select {
	case <-newSendStarted:
	case <-time.After(8 * time.Second):
		cancel()
		<-done
		t.Fatal("reloaded digest did not reach Telegram")
	}
	cancel()

	// Then
	require.NoError(t, <-done)
	alibPathsMu.Lock()
	gotAlibRequests := append([]reloadAlibRequest(nil), alibRequests...)
	alibPathsMu.Unlock()
	assertReloadedDownloadDelay(t, gotAlibRequests)
	require.FileExists(t, filepath.Join(root, "old.db"))
	require.FileExists(t, filepath.Join(root, "new.db"))
}

func assertReloadedDownloadDelay(t *testing.T, requests []reloadAlibRequest) {
	t.Helper()
	paths := make([]string, 0, len(requests))
	var detektivy, poisk time.Time
	for _, request := range requests {
		paths = append(paths, request.Path)
		switch request.Path {
		case "/detektivy.phtml":
			detektivy = request.At
		case "/poisk.phtml":
			poisk = request.At
		}
	}
	require.Contains(t, paths, "/tramka.phtml")
	require.False(t, detektivy.IsZero())
	require.False(t, poisk.IsZero())
	require.GreaterOrEqual(t, poisk.Sub(detektivy), 75*time.Millisecond)
}

type reloadLogSignal struct {
	pending chan struct{}
	once    sync.Once
}

func (signal *reloadLogSignal) Write(data []byte) (int, error) {
	if bytes.Contains(data, []byte(`"msg":"config.reload_pending"`)) {
		signal.once.Do(func() { close(signal.pending) })
	}
	return len(data), nil
}

func Test_run_forget_latest_only_requires_state_path(t *testing.T) {
	// Given
	statePath := filepath.Join(t.TempDir(), "state.db")
	setEnvironmentAbsentDigestConfiguration(t)
	setMaintenanceConfig(t, statePath, "-100123")
	useCommandLine(t, "-forget-latest", "1", "-chat", "-100123", "-config", os.Getenv("ALIB_TEST_CONFIG"))
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
	setMaintenanceConfig(t, statePath, "-100123")
	books := make([]alib.Book, 8)
	for index := range books {
		books[index] = alib.Book{BuyURL: fmt.Sprintf("https://example.com/book-%d", index)}
	}
	state, err := store.Open(statePath, time.Now())
	require.NoError(t, err)
	_, err = state.RecordDiscovered(context.Background(), books, time.Now())
	require.NoError(t, err)
	require.NoError(t, state.Close())
	useCommandLine(t, "-forget-latest", "6", "-chat", "-100123", "-config", os.Getenv("ALIB_TEST_CONFIG"))

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
	configPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[[chats]]\nchat_id = \"-100123\"\ntelegram_token = \"test-token\"\n"), 0o600))
	useCommandLine(t, "-once", "-config", configPath)

	// When
	err := run(slog.New(slog.DiscardHandler))

	// Then
	require.ErrorIs(t, err, config.ErrInvalid)
	require.ErrorContains(t, err, "categories, filters, and queries")
}

func setMaintenanceConfig(t *testing.T, statePath, chatID string) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	content := fmt.Sprintf("state_path = %q\n\n[[chats]]\nchat_id = %q\nstate_file = %q\n",
		filepath.Dir(statePath), chatID, filepath.Base(statePath))
	require.NoError(t, os.WriteFile(configPath, []byte(content), 0o600))
	t.Setenv("ALIB_TEST_CONFIG", configPath)
}

func writeMainConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
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
	t.Setenv("ALIB_PUBLISHERS", "")
	routeTelegramRequestsTo(t, telegramServer.URL)
	t.Setenv("HTTP_TIMEOUT", "2s")
	t.Setenv("ALIB_MAX_RETRIES", "0")
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
	const (
		recoveredPath  = "/recovered-book.html"
		truncatedPath  = "/truncated-book.html"
		oversizedPath  = "/oversized-book.html"
		messageLimit   = 500
		truncatedTitle = "Сокращаемая книга"
	)
	truncatedContent := strings.Repeat("Длинное описание книги. ", 100)
	oversizedTitle := strings.Repeat("Обязательное поле ", 1000)
	alibRequests := make(chan alibRequest, 4)
	alibServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		alibRequests <- alibRequest{Path: request.URL.Path, RawQuery: request.URL.RawQuery}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case request.URL.Path == "/first.phtml":
			_, err := writer.Write([]byte(
				`<p><a href="/bs.php4?bs=Seller">BS - Seller</a><br>` +
					`<b>Сбойное объявление.</b> М., 2026 г. <a href="` + recoveredPath + `"><b>Купить</b></a></p>` +
					`<p><b>` + truncatedTitle + `.</b> Цена: 100 руб. ` +
					`<a href="` + truncatedPath + `"><b>Купить</b></a><br>` +
					truncatedContent + `</p>` +
					`<p><b>` + oversizedTitle + `.</b> Цена: 200 руб. ` +
					`<a href="` + oversizedPath + `"><b>Купить</b></a></p>`,
			))
			assert.NoError(t, err)
		case request.URL.Path == "/broken.phtml":
			writer.WriteHeader(http.StatusBadGateway)
		case request.URL.Path == "/findp.php4" && request.URL.Query().Get("seria") != "changed":
			assert.Equal(t, "seria=%D1%E5%F0%E8%FF%2C+%F2%EE%EC%E0&lday=7", request.URL.RawQuery)
			decodedSeries, decodeErr := charmap.Windows1251.NewDecoder().String(request.URL.Query().Get("seria"))
			assert.NoError(t, decodeErr)
			assert.Equal(t, "Серия, тома", decodedSeries)
			_, err := writer.Write([]byte(
				testutil.ListingPage("Восстановленная книга", recoveredPath, "999 руб."),
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

	telegramRequests := make(chan telegramRequest, 8)
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
	t.Setenv("MESSAGE_LIMIT", strconv.Itoa(messageLimit))
	writeEnvironmentConfig(t, os.Getenv("ALIB_TEST_CONFIG"))
	settings, err := config.Load(os.Getenv("ALIB_TEST_CONFIG"))
	require.NoError(t, err)
	require.Equal(t, []string{
		"https://www.alib.ru/first.phtml?tnew=7",
		"https://www.alib.ru/broken.phtml?tnew=7",
		"https://alib.ru/findp.php4?seria=%D1%E5%F0%E8%FF%2C+%F2%EE%EC%E0&lday=7",
		"https://alib.ru/findp.php4?seria=changed&lday=7",
	}, settings.Chats[0].AlibURLs)
	settings.Chats[0].AlibURLs = localAlibURLs(t, alibServer.URL, settings.Chats[0].AlibURLs)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// When
	err = runWithConfig(logger, settings, true)

	// Then
	require.NoError(t, err)
	require.Equal(t, []alibRequest{
		{Path: "/first.phtml", RawQuery: "tnew=7"},
		{Path: "/broken.phtml", RawQuery: "tnew=7"},
		{Path: "/findp.php4", RawQuery: "seria=%D1%E5%F0%E8%FF%2C+%F2%EE%EC%E0&lday=7"},
		{Path: "/findp.php4", RawQuery: "seria=changed&lday=7"},
	}, []alibRequest{<-alibRequests, <-alibRequests, <-alibRequests, <-alibRequests})
	firstCycleMessages := make([]telegramRequest, len(telegramRequests))
	for index := range firstCycleMessages {
		firstCycleMessages[index] = <-telegramRequests
	}
	require.NotEmpty(t, firstCycleMessages)
	firstCycleHTML := make([]string, 0, len(firstCycleMessages))
	for index, request := range firstCycleMessages {
		require.Equal(t, "/bottest-token/sendRichMessage", request.Path)
		require.LessOrEqual(t, testutil.DisplayedRuneCount(t, request.Message.RichMessage.HTML), messageLimit)
		require.NotRegexp(t, `[\n]`, request.Message.RichMessage.HTML)
		if index == len(firstCycleMessages)-1 {
			requireRefreshButton(t, request.Message)
		} else {
			require.Nil(t, request.Message.ReplyMarkup)
		}
		firstCycleHTML = append(firstCycleHTML, request.Message.RichMessage.HTML)
	}
	combinedHTML := strings.Join(firstCycleHTML, "\n")
	require.Contains(t, combinedHTML, alibServer.URL+recoveredPath)
	require.Contains(t, combinedHTML, alibServer.URL+truncatedPath)
	require.NotContains(t, combinedHTML, alibServer.URL+oversizedPath)
	require.Contains(t, combinedHTML, "Восстановленная книга")
	require.NotContains(t, combinedHTML, "Сбойное объявление")
	require.Contains(t, combinedHTML, "…")
	require.Equal(t, 1, strings.Count(combinedHTML, "Не удалось обработать книг: 1"))
	logOutput := logs.String()
	require.Equal(t, 3, strings.Count(logOutput, `"msg":"alib.page_downloaded"`))
	require.Equal(t, 1, strings.Count(logOutput, `"msg":"alib.page_download_failed"`))
	require.Equal(t, 2, strings.Count(logOutput, `"msg":"alib.page_parsed"`))
	require.Equal(t, 1, strings.Count(logOutput, `"msg":"alib.page_parse_failed"`))
	require.Contains(t, logOutput, `"msg":"alib.page_downloaded","chat_id":"-100123","index":0,"url":"`+alibServer.URL+`/first.phtml?tnew=7"`)
	require.Contains(t, logOutput, `"msg":"alib.page_downloaded","chat_id":"-100123","index":2,"url":"`+alibServer.URL+`/findp.php4?seria=%D1%E5%F0%E8%FF%2C+%F2%EE%EC%E0&lday=7"`)
	require.Contains(t, logOutput, `"msg":"alib.page_downloaded","chat_id":"-100123","index":3,"url":"`+alibServer.URL+`/findp.php4?seria=changed&lday=7"`)
	require.Contains(t, logOutput, `"msg":"alib.page_download_failed","chat_id":"-100123","index":1,"url":"`+alibServer.URL+`/broken.phtml?tnew=7"`)
	require.Contains(t, logOutput, `"msg":"alib.page_parsed","chat_id":"-100123","index":0,"url":"`+alibServer.URL+`/first.phtml?tnew=7","books":2`)
	require.Contains(t, logOutput, `"msg":"alib.page_parsed","chat_id":"-100123","index":2,"url":"`+alibServer.URL+`/findp.php4?seria=%D1%E5%F0%E8%FF%2C+%F2%EE%EC%E0&lday=7","books":1`)
	require.Contains(t, logOutput, `"msg":"alib.page_parse_failed","chat_id":"-100123","index":3,"url":"`+alibServer.URL+`/findp.php4?seria=changed&lday=7"`)
	require.Less(t,
		strings.LastIndex(logOutput, `"msg":"alib.page_downloaded"`),
		strings.Index(logOutput, `"msg":"alib.page_parsed"`),
	)
	require.NotContains(t, logOutput, `"msg":"alib.page_failed"`)
	require.Contains(t, logOutput, `"msg":"digest.completed"`)
	require.Contains(t, logOutput, `"fetched":3`)
	require.Contains(t, logOutput, `"new":2`)
	require.Contains(t, logOutput, `"failed":1`)
	require.Contains(t, logOutput, `"sent":2`)

	state, err := store.Open(statePath, time.Now())
	require.NoError(t, err)
	existing, err := state.Existing(context.Background(), []alib.Book{
		{BuyURL: alibServer.URL + recoveredPath},
		{BuyURL: alibServer.URL + truncatedPath},
		{BuyURL: alibServer.URL + oversizedPath},
	})
	require.NoError(t, err)
	require.Equal(t, []bool{true, true, false}, existing)
	pending, err := state.Pending(context.Background())
	require.NoError(t, err)
	require.NoError(t, state.Close())
	require.Empty(t, pending)

	// When
	err = runWithConfig(logger, settings, true)

	// Then
	require.NoError(t, err)
	require.Len(t, telegramRequests, 1)
	secondCycleMessage := (<-telegramRequests).Message
	require.Contains(t, secondCycleMessage.RichMessage.HTML, "Не удалось обработать книг: 1")
	require.NotContains(t, secondCycleMessage.RichMessage.HTML, alibServer.URL+recoveredPath)
	require.NotContains(t, secondCycleMessage.RichMessage.HTML, alibServer.URL+truncatedPath)
	require.NotContains(t, secondCycleMessage.RichMessage.HTML, alibServer.URL+oversizedPath)
	requireRefreshButton(t, secondCycleMessage)
	require.Equal(t, 1, strings.Count(secondCycleMessage.RichMessage.HTML, "Не удалось обработать книг: 1"))
}

func Test_run_once_sends_notification_for_all_correct_empty_pages(t *testing.T) {
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
	telegramRequests := make(chan telegramRequest, 1)
	telegramServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		telegramRequests <- telegramRequest{Message: decodeTelegramMessage(t, request), Path: request.URL.Path}
		writer.Header().Set("Content-Type", "application/json")
		_, writeErr := writer.Write([]byte(`{"ok":true,"result":{}}`))
		assert.NoError(t, writeErr)
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
	require.Len(t, telegramRequests, 1)
	request := <-telegramRequests
	require.Equal(t, "/bottest-token/sendRichMessage", request.Path)
	require.Equal(t, "Новых книг не обнаружено.", request.Message.RichMessage.HTML)
	require.False(t, request.Message.DisableNotification)
	requireRefreshButton(t, request.Message)
	logOutput := logs.String()
	require.Equal(t, 2, strings.Count(logOutput, `"msg":"alib.page_downloaded"`))
	require.Equal(t, 2, strings.Count(logOutput, `"msg":"alib.page_parsed"`))
	require.Contains(t, logOutput, `"msg":"alib.page_downloaded","chat_id":"-100123","index":0,"url":"`+alibServer.URL+`/empty-one?first=true"`)
	require.Contains(t, logOutput, `"msg":"alib.page_downloaded","chat_id":"-100123","index":1,"url":"`+alibServer.URL+`/empty-two?second=true"`)
	require.Contains(t, logOutput, `"msg":"alib.page_parsed","chat_id":"-100123","index":0,"url":"`+alibServer.URL+`/empty-one?first=true","books":0`)
	require.Contains(t, logOutput, `"msg":"alib.page_parsed","chat_id":"-100123","index":1,"url":"`+alibServer.URL+`/empty-two?second=true","books":0`)
	require.Contains(t, logOutput, `"msg":"digest.completed"`)
	require.Contains(t, logOutput, `"fetched":0`)
	require.Contains(t, logOutput, `"new":0`)
	require.Contains(t, logOutput, `"sent":0`)
	require.NotContains(t, logOutput, "alib.page_failed")
}

func Test_run_once_recovers_from_transient_alib_error(t *testing.T) {
	// Given
	useOnceMode(t)
	var requests atomic.Int32
	alibServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			writer.WriteHeader(http.StatusBadGateway)

			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err := writer.Write([]byte(`<p><b>Восстановленная книга.</b> <a href="/book.html"><b>Купить</b></a></p>`))
		assert.NoError(t, err)
	}))
	t.Cleanup(alibServer.Close)
	telegramRequests := make(chan telegramRequest, 1)
	telegramServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		telegramRequests <- telegramRequest{Message: decodeTelegramMessage(t, request), Path: request.URL.Path}
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"ok":true,"result":{}}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(telegramServer.Close)
	setRunEnvironment(t, telegramServer.URL, filepath.Join(t.TempDir(), "state.db"))
	t.Setenv("ALIB_MAX_RETRIES", "1")

	// When
	err := runWithAlibURLs(t, slog.New(slog.DiscardHandler), alibServer.URL+"/tramka.phtml?tnew=7")

	// Then
	require.NoError(t, err)
	require.Equal(t, int32(2), requests.Load())
	require.Len(t, telegramRequests, 1)
	message := (<-telegramRequests).Message
	require.Contains(t, message.RichMessage.HTML, "Восстановленная книга")
	requireRefreshButton(t, message)
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
	require.Contains(t, logOutput, `"msg":"alib.page_download_failed","chat_id":"-100123","index":0,"url":"`+
		alibServer.URL+`/status-one?status=one"`)
	require.Contains(t, logOutput, `"msg":"alib.page_downloaded","chat_id":"-100123","index":1,"url":"`+
		alibServer.URL+`/broken?scope=broken"`)
	require.Contains(t, logOutput, `"msg":"alib.page_parse_failed","chat_id":"-100123","index":1,"url":"`+
		alibServer.URL+`/broken?scope=broken"`)
	require.Contains(t, logOutput, `"msg":"alib.page_download_failed","chat_id":"-100123","index":2,"url":"`+
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
	configPath := os.Getenv("ALIB_TEST_CONFIG")
	if configPath == "" {
		configPath = filepath.Join(t.TempDir(), "config.toml")
		t.Setenv("ALIB_TEST_CONFIG", configPath)
	}
	writeEnvironmentConfig(t, configPath)
	settings, err := config.Load(configPath)
	require.NoError(t, err)
	settings.Chats[0].AlibURLs = append([]string(nil), endpoints...)

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

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func routeAlibRequestsTo(t *testing.T, base string) {
	t.Helper()
	target, err := url.Parse(base)
	require.NoError(t, err)
	originalTransport := http.DefaultTransport
	t.Cleanup(func() {
		http.DefaultTransport = originalTransport
	})
	http.DefaultTransport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "www.alib.ru" && request.URL.Host != "alib.ru" {
			return originalTransport.RoundTrip(request)
		}

		routedRequest := request.Clone(request.Context())
		routedURL := *request.URL
		routedURL.Scheme = target.Scheme
		routedURL.Host = target.Host
		routedRequest.URL = &routedURL

		return originalTransport.RoundTrip(routedRequest)
	})
}

func routeTelegramRequestsTo(t *testing.T, base string) {
	t.Helper()
	target, err := url.Parse(base)
	require.NoError(t, err)
	originalTransport := http.DefaultTransport
	t.Cleanup(func() {
		http.DefaultTransport = originalTransport
	})
	http.DefaultTransport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "api.telegram.org" {
			return originalTransport.RoundTrip(request)
		}

		routedRequest := request.Clone(request.Context())
		routedURL := *request.URL
		routedURL.Scheme = target.Scheme
		routedURL.Host = target.Host
		routedRequest.URL = &routedURL

		return originalTransport.RoundTrip(routedRequest)
	})
}

func useCommandLine(t *testing.T, arguments ...string) {
	t.Helper()

	originalArgs := os.Args
	t.Cleanup(func() {
		os.Args = originalArgs
	})
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
		"ALIB_PUBLISHERS",
		"HTTP_TIMEOUT",
		"MESSAGE_LIMIT",
		"RUN_ON_STARTUP",
		"FRESH_BOOKS",
		"ALIB_MAX_RETRIES",
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

func writeTelegramResponse(t *testing.T, writer http.ResponseWriter, body string) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	_, err := io.WriteString(writer, body)
	require.NoError(t, err)
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
	t.Setenv("ALIB_PUBLISHERS", "")
	routeTelegramRequestsTo(t, telegramAPIBase)
	t.Setenv("HTTP_TIMEOUT", "2s")
	t.Setenv("ALIB_MAX_RETRIES", "0")
	t.Setenv("MESSAGE_LIMIT", "4000")
	configPath := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("ALIB_TEST_CONFIG", configPath)
	writeEnvironmentConfig(t, configPath)
	os.Args = append(os.Args, "-config", configPath)
}

func writeEnvironmentConfig(t *testing.T, configPath string) {
	t.Helper()
	statePath := os.Getenv("STATE_PATH")
	stateDir, stateFile := filepath.Dir(statePath), filepath.Base(statePath)
	if statePath == "" {
		stateDir, stateFile = ".", "state.db"
	}
	categories := csvArray(t, os.Getenv("ALIB_CATEGORIES"))
	series := csvArray(t, os.Getenv("ALIB_SERIES"))
	publishers := csvArray(t, os.Getenv("ALIB_PUBLISHERS"))
	var builder strings.Builder
	_, err := fmt.Fprintf(&builder, "state_path = %s\ncron_schedule = %s\ntimezone = %s\nrun_on_startup = %s\n",
		strconv.Quote(stateDir), strconv.Quote(valueOr(os.Getenv("CRON_SCHEDULE"), "0 0 * * *")),
		strconv.Quote(valueOr(os.Getenv("TIMEZONE"), "UTC")), valueOr(os.Getenv("RUN_ON_STARTUP"), "true"))
	require.NoError(t, err)
	if value := os.Getenv("HTTP_TIMEOUT"); value != "" {
		_, err = fmt.Fprintf(&builder, "http_timeout = %s\n", strconv.Quote(value))
		require.NoError(t, err)
	}
	if value := os.Getenv("ALIB_MAX_RETRIES"); value != "" {
		_, err = fmt.Fprintf(&builder, "alib_max_retries = %s\n", value)
		require.NoError(t, err)
	}
	if value := os.Getenv("MESSAGE_LIMIT"); value != "" {
		_, err = fmt.Fprintf(&builder, "message_limit = %s\n", value)
		require.NoError(t, err)
	}
	if freshBooks := os.Getenv("FRESH_BOOKS"); freshBooks != "" {
		_, err = fmt.Fprintf(&builder, "fresh_books = %s\n", strconv.Quote(freshBooks))
		require.NoError(t, err)
	}
	_, err = fmt.Fprintf(&builder, "\n[[chats]]\nchat_id = %s\ntelegram_token = %s\nstate_file = %s\ncategories = %s\n",
		strconv.Quote(os.Getenv("TELEGRAM_CHAT_ID")), strconv.Quote(os.Getenv("TELEGRAM_BOT_TOKEN")),
		strconv.Quote(stateFile), tomlArray(categories))
	require.NoError(t, err)
	if len(series) > 0 || len(publishers) > 0 {
		_, err = builder.WriteString("\n[chats.filters]\n")
		require.NoError(t, err)
		if len(series) > 0 {
			_, err = fmt.Fprintf(&builder, "seria = %s\n", tomlArray(series))
			require.NoError(t, err)
		}
		if len(publishers) > 0 {
			_, err = fmt.Fprintf(&builder, "izdat = %s\n", tomlArray(publishers))
			require.NoError(t, err)
		}
	}
	require.NoError(t, os.WriteFile(configPath, []byte(builder.String()), 0o600))
}

func csvArray(t *testing.T, value string) []string {
	t.Helper()
	if value == "" {
		return nil
	}
	items, err := csv.NewReader(strings.NewReader(value)).Read()
	require.NoError(t, err)
	return items
}

func tomlArray(values []string) string {
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = strconv.Quote(strings.TrimSpace(value))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func unsetEnvironment(t *testing.T, key string) {
	t.Helper()

	t.Setenv(key, "")
	require.NoError(t, os.Unsetenv(key))
}
