package config_test

import (
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kemko/alib-fetcher/internal/config"

	"github.com/stretchr/testify/require"
)

func Test_Load_applies_service_defaults(t *testing.T) {
	// Given
	setEnvironment(t, map[string]string{
		"TELEGRAM_BOT_TOKEN":    "token",
		"TELEGRAM_CHAT_ID":      "-100123",
		"CRON_SCHEDULE":         "",
		"TIMEZONE":              "",
		"STATE_PATH":            "",
		"ALIB_REQUEST_INTERVAL": "",
		"TELEGRAM_API_BASE":     "",
		"HTTP_TIMEOUT":          "",
		"MESSAGE_LIMIT":         "",
		"RUN_ON_STARTUP":        "",
		"FRESH_BOOKS":           "",
	})

	// When
	loaded, err := config.Load()

	// Then
	require.NoError(t, err)
	require.Equal(t, "0 0 * * *", loaded.CronSpec())
	require.Equal(t, "Europe/Moscow", loaded.Location.String())
	require.Equal(t, "/var/lib/alib-fetcher/state.db", loaded.StatePath)
	require.Equal(t, []string{"https://www.alib.ru/tramka.phtml?tnew=7"}, loaded.AlibURLs)
	require.Equal(t, time.Second, loaded.AlibRequestInterval)
	require.Equal(t, "https://api.telegram.org", loaded.TelegramAPIBase)
	require.Equal(t, 30*time.Second, loaded.HTTPTimeout)
	require.Equal(t, 32000, loaded.MessageLimit)
	require.True(t, loaded.RunOnStartup)
	require.Nil(t, loaded.FreshBooks)
}

func Test_Load_validates_message_limit(t *testing.T) {
	testCases := map[string]struct {
		value     string
		wantError bool
		expected  int
	}{
		"hard limit": {
			value:    "32768",
			expected: 32768,
		},
		"above hard limit": {
			value:     "32769",
			wantError: true,
		},
		"below minimum": {
			value:     "63",
			wantError: true,
		},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			// Given
			setEnvironment(t, map[string]string{
				"TELEGRAM_BOT_TOKEN": "token",
				"TELEGRAM_CHAT_ID":   "-100123",
				"MESSAGE_LIMIT":      testCase.value,
			})

			// When
			loaded, err := config.Load()

			// Then
			if testCase.wantError {
				require.ErrorIs(t, err, config.ErrInvalid)
				require.ErrorContains(t, err, "MESSAGE_LIMIT")
				require.Empty(t, loaded)
				return
			}
			require.NoError(t, err)
			require.Equal(t, testCase.expected, loaded.MessageLimit)
		})
	}
}

func Test_LoadStatePath_reads_environment_without_full_configuration(t *testing.T) {
	// Given
	const statePath = "/tmp/alib-fetcher-maintenance.db"
	setEnvironment(t, map[string]string{
		"STATE_PATH":            statePath,
		"TELEGRAM_BOT_TOKEN":    "",
		"TELEGRAM_CHAT_ID":      "",
		"CRON_SCHEDULE":         "not a cron expression",
		"TIMEZONE":              "not a timezone",
		"HTTP_TIMEOUT":          "not a duration",
		"MESSAGE_LIMIT":         "not a number",
		"RUN_ON_STARTUP":        "not a boolean",
		"ALIB_REQUEST_INTERVAL": "",
		"TELEGRAM_API_BASE":     "",
		"FRESH_BOOKS":           "",
	})

	// When
	loaded := config.LoadStatePath()

	// Then
	require.Equal(t, statePath, loaded)
}

func Test_LoadStatePath_uses_default(t *testing.T) {
	// Given
	unsetEnvironment(t, "STATE_PATH")

	// When
	loaded := config.LoadStatePath()

	// Then
	require.Equal(t, "/var/lib/alib-fetcher/state.db", loaded)
}

func Test_Load_disables_fresh_books_threshold_when_unset_or_empty(t *testing.T) {
	testCases := map[string]bool{
		"unset": false,
		"empty": true,
	}

	for name, setValue := range testCases {
		t.Run(name, func(t *testing.T) {
			// Given
			setEnvironment(t, map[string]string{
				"TELEGRAM_BOT_TOKEN": "token",
				"TELEGRAM_CHAT_ID":   "-100123",
			})
			if setValue {
				t.Setenv("FRESH_BOOKS", "")
			} else {
				unsetEnvironment(t, "FRESH_BOOKS")
			}

			// When
			loaded, err := config.Load()

			// Then
			require.NoError(t, err)
			require.Nil(t, loaded.FreshBooks)
		})
	}
}

func Test_Load_parses_fresh_books_policy(t *testing.T) {
	testCases := map[string]struct {
		value       string
		currentYear int
		lowerYear   int
	}{
		"age": {
			value:       "age:5",
			currentYear: 2026,
			lowerYear:   2021,
		},
		"age zero": {
			value:       "age:0",
			currentYear: 2026,
			lowerYear:   2026,
		},
		"since": {
			value:       "since:2021",
			currentYear: 2026,
			lowerYear:   2021,
		},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			// Given
			setEnvironment(t, map[string]string{
				"TELEGRAM_BOT_TOKEN": "token",
				"TELEGRAM_CHAT_ID":   "-100123",
				"FRESH_BOOKS":        testCase.value,
			})

			// When
			loaded, err := config.Load()

			// Then
			require.NoError(t, err)
			require.NotNil(t, loaded.FreshBooks)
			require.Equal(t, testCase.lowerYear, loaded.FreshBooks.LowerYear(testCase.currentYear))
		})
	}
}

func Test_Load_rejects_invalid_fresh_books_policy(t *testing.T) {
	testCases := []string{
		"age:-1",
		"age:+5",
		"age:1.5",
		"age:",
		"since:999",
		"since:0000",
		"since:10000",
		"since:20a1",
		"fresh:2021",
	}

	for _, value := range testCases {
		t.Run(value, func(t *testing.T) {
			// Given
			setEnvironment(t, map[string]string{
				"TELEGRAM_BOT_TOKEN": "token",
				"TELEGRAM_CHAT_ID":   "-100123",
				"FRESH_BOOKS":        value,
			})

			// When
			loaded, err := config.Load()

			// Then
			require.ErrorIs(t, err, config.ErrInvalid)
			require.ErrorContains(t, err, "FRESH_BOOKS")
			require.Empty(t, loaded)
		})
	}
}

func Test_Load_parses_custom_schedule(t *testing.T) {
	// Given
	setEnvironment(t, map[string]string{
		"TELEGRAM_BOT_TOKEN":    "token",
		"TELEGRAM_CHAT_ID":      "@books",
		"CRON_SCHEDULE":         "*/15 8-18 * * 1-5",
		"TIMEZONE":              "Asia/Tbilisi",
		"STATE_PATH":            "/tmp/custom.db",
		"ALIB_CATEGORIES":       "books",
		"ALIB_REQUEST_INTERVAL": "250ms",
		"TELEGRAM_API_BASE":     "https://telegram.example.test",
		"HTTP_TIMEOUT":          "15s",
		"MESSAGE_LIMIT":         "3500",
		"RUN_ON_STARTUP":        "false",
	})

	// When
	loaded, err := config.Load()

	// Then
	require.NoError(t, err)
	require.Equal(t, "*/15 8-18 * * 1-5", loaded.CronSpec())
	require.Equal(t, "Asia/Tbilisi", loaded.Location.String())
	require.Equal(t, 15*time.Second, loaded.HTTPTimeout)
	require.Equal(t, 250*time.Millisecond, loaded.AlibRequestInterval)
	require.Equal(t, 3500, loaded.MessageLimit)
	require.False(t, loaded.RunOnStartup)
}

func Test_Load_accepts_positive_HTTP_timeout(t *testing.T) {
	// Given
	setEnvironment(t, map[string]string{
		"TELEGRAM_BOT_TOKEN": "token",
		"TELEGRAM_CHAT_ID":   "-100123",
		"HTTP_TIMEOUT":       "1ms",
	})

	// When
	loaded, err := config.Load()

	// Then
	require.NoError(t, err)
	require.Equal(t, time.Millisecond, loaded.HTTPTimeout)
}

func Test_Load_ignores_removed_Slink_configuration(t *testing.T) {
	// Given
	setEnvironment(t, map[string]string{
		"TELEGRAM_BOT_TOKEN": "token",
		"TELEGRAM_CHAT_ID":   "-100123",
		"SLINK_URL":          "not a URL",
		"SLINK_API_KEY":      "not an API key",
		"SLINK_TAG_ID":       "not a UUID",
	})

	// When
	loaded, err := config.Load()

	// Then
	require.NoError(t, err)
	require.Equal(t, "token", loaded.TelegramToken)
}

func Test_Load_builds_AlibURLs_from_categories_and_series(t *testing.T) {
	testCases := map[string]struct {
		categories string
		series     string
		want       []string
	}{
		"categories only": {
			categories: "tramka,deti,tramka",
			want: []string{
				"https://www.alib.ru/tramka.phtml?tnew=7",
				"https://www.alib.ru/deti.phtml?tnew=7",
				"https://www.alib.ru/tramka.phtml?tnew=7",
			},
		},
		"series only": {
			series: `"Фантастика, новинки"`,
			want: []string{
				"https://alib.ru/findp.php4?seria=%D0%A4%D0%B0%D0%BD%D1%82%D0%B0%D1%81%D1%82%D0%B8%D0%BA%D0%B0%2C+%D0%BD%D0%BE%D0%B2%D0%B8%D0%BD%D0%BA%D0%B8&lday=7",
			},
		},
		"both preserve order": {
			categories: " tramka, deti ",
			series:     `серия,"другая, том"`,
			want: []string{
				"https://www.alib.ru/tramka.phtml?tnew=7",
				"https://www.alib.ru/deti.phtml?tnew=7",
				"https://alib.ru/findp.php4?seria=%D1%81%D0%B5%D1%80%D0%B8%D1%8F&lday=7",
				"https://alib.ru/findp.php4?seria=%D0%B4%D1%80%D1%83%D0%B3%D0%B0%D1%8F%2C+%D1%82%D0%BE%D0%BC&lday=7",
			},
		},
		"quoted comma and special characters": {
			series: `"Серия, тома", A&B / C`,
			want: []string{
				"https://alib.ru/findp.php4?seria=%D0%A1%D0%B5%D1%80%D0%B8%D1%8F%2C+%D1%82%D0%BE%D0%BC%D0%B0&lday=7",
				"https://alib.ru/findp.php4?seria=A%26B+%2F+C&lday=7",
			},
		},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			setEnvironment(t, map[string]string{
				"TELEGRAM_BOT_TOKEN": "token",
				"TELEGRAM_CHAT_ID":   "-100123",
				"ALIB_CATEGORIES":    testCase.categories,
				"ALIB_SERIES":        testCase.series,
			})

			loaded, err := config.Load()

			require.NoError(t, err)
			require.Equal(t, testCase.want, loaded.AlibURLs)
		})
	}
}

func Test_Load_rejects_invalid_Alib_tracking_configuration(t *testing.T) {
	testCases := map[string]struct {
		categories string
		series     string
		variable   string
	}{
		"both absent": {
			variable: "ALIB_CATEGORIES and ALIB_SERIES",
		},
		"old URL only": {
			variable: "ALIB_CATEGORIES and ALIB_SERIES",
		},
		"empty category item": {
			categories: "tramka,,deti",
			variable:   "ALIB_CATEGORIES",
		},
		"empty series item": {
			series:   "series, ,other",
			variable: "ALIB_SERIES",
		},
		"malformed CSV": {
			series:   `"unterminated`,
			variable: "ALIB_SERIES",
		},
		"multiple CSV records": {
			categories: "tramka\ndeti",
			variable:   "ALIB_CATEGORIES",
		},
		"invalid category": {
			categories: "tram-ka",
			variable:   "ALIB_CATEGORIES",
		},
		"unicode category": {
			categories: "книги",
			variable:   "ALIB_CATEGORIES",
		},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			setEnvironment(t, map[string]string{
				"TELEGRAM_BOT_TOKEN": "token",
				"TELEGRAM_CHAT_ID":   "-100123",
				"ALIB_CATEGORIES":    testCase.categories,
				"ALIB_SERIES":        testCase.series,
				"ALIB_URL":           "https://example.com/old",
			})
			if testCase.categories == "" {
				unsetEnvironment(t, "ALIB_CATEGORIES")
			}
			if testCase.series == "" {
				unsetEnvironment(t, "ALIB_SERIES")
			}
			if name == "old URL only" {
				unsetEnvironment(t, "ALIB_CATEGORIES")
				unsetEnvironment(t, "ALIB_SERIES")
			}

			loaded, err := config.Load()

			require.ErrorIs(t, err, config.ErrInvalid)
			require.ErrorContains(t, err, testCase.variable)
			require.Empty(t, loaded)
		})
	}
}

func Test_Load_rejects_invalid_HTTP_timeout(t *testing.T) {
	testCases := []string{"invalid", "0s", "-1s"}

	for _, timeout := range testCases {
		t.Run(timeout, func(t *testing.T) {
			// Given
			setEnvironment(t, map[string]string{
				"TELEGRAM_BOT_TOKEN": "token",
				"TELEGRAM_CHAT_ID":   "-100123",
				"HTTP_TIMEOUT":       timeout,
			})

			// When
			loaded, err := config.Load()

			// Then
			require.ErrorIs(t, err, config.ErrInvalid)
			require.ErrorContains(t, err, "HTTP_TIMEOUT")
			require.Empty(t, loaded)
		})
	}
}

func Test_Load_accepts_non_negative_Alib_request_interval(t *testing.T) {
	testCases := map[string]time.Duration{
		"zero":     0,
		"positive": 250 * time.Millisecond,
	}

	for name, expected := range testCases {
		t.Run(name, func(t *testing.T) {
			// Given
			setEnvironment(t, map[string]string{
				"TELEGRAM_BOT_TOKEN":    "token",
				"TELEGRAM_CHAT_ID":      "-100123",
				"ALIB_REQUEST_INTERVAL": expected.String(),
			})

			// When
			loaded, err := config.Load()

			// Then
			require.NoError(t, err)
			require.Equal(t, expected, loaded.AlibRequestInterval)
		})
	}
}

func Test_Load_rejects_invalid_Alib_request_interval(t *testing.T) {
	for _, value := range []string{"invalid", "-1s"} {
		t.Run(value, func(t *testing.T) {
			// Given
			setEnvironment(t, map[string]string{
				"TELEGRAM_BOT_TOKEN":    "token",
				"TELEGRAM_CHAT_ID":      "-100123",
				"ALIB_REQUEST_INTERVAL": value,
			})

			// When
			loaded, err := config.Load()

			// Then
			require.ErrorIs(t, err, config.ErrInvalid)
			require.ErrorContains(t, err, "ALIB_REQUEST_INTERVAL")
			require.Empty(t, loaded)
		})
	}
}

func Test_Load_accepts_valid_telegram_chat_id(t *testing.T) {
	testCases := []string{
		strconv.FormatInt(math.MinInt64, 10),
		"-100123",
		"0",
		"+123",
		strconv.FormatInt(math.MaxInt64, 10),
		"@channel",
	}

	for _, chatID := range testCases {
		t.Run(chatID, func(t *testing.T) {
			// Given
			setEnvironment(t, map[string]string{
				"TELEGRAM_BOT_TOKEN": "token",
				"TELEGRAM_CHAT_ID":   chatID,
			})

			// When
			loaded, err := config.Load()

			// Then
			require.NoError(t, err)
			require.Equal(t, chatID, loaded.TelegramChatID)
		})
	}
}

func Test_Load_rejects_invalid_telegram_chat_id(t *testing.T) {
	testCases := []string{
		"chat",
		"@",
		"@channel name",
		" @channel",
		"@channel\n",
		"123 456",
		"9223372036854775808",
		"-9223372036854775809",
	}

	for _, chatID := range testCases {
		t.Run(strings.ReplaceAll(chatID, "\n", `\n`), func(t *testing.T) {
			// Given
			const token = "secret-token-value"
			setEnvironment(t, map[string]string{
				"TELEGRAM_BOT_TOKEN": token,
				"TELEGRAM_CHAT_ID":   chatID,
			})

			// When
			loaded, err := config.Load()

			// Then
			require.ErrorIs(t, err, config.ErrInvalid)
			require.ErrorContains(t, err, "TELEGRAM_CHAT_ID")
			require.NotContains(t, err.Error(), token)
			require.Empty(t, loaded)
		})
	}
}

func Test_Load_rejects_invalid_run_on_startup(t *testing.T) {
	// Given
	setEnvironment(t, map[string]string{
		"TELEGRAM_BOT_TOKEN": "token",
		"TELEGRAM_CHAT_ID":   "-100123",
		"RUN_ON_STARTUP":     "sometimes",
	})

	// When
	loaded, err := config.Load()

	// Then
	require.ErrorIs(t, err, config.ErrInvalid)
	require.Empty(t, loaded)
}

func Test_Load_rejects_invalid_schedule(t *testing.T) {
	// Given
	setEnvironment(t, map[string]string{
		"TELEGRAM_BOT_TOKEN": "token",
		"TELEGRAM_CHAT_ID":   "-100123",
		"CRON_SCHEDULE":      "not a cron expression",
	})

	// When
	loaded, err := config.Load()

	// Then
	require.ErrorIs(t, err, config.ErrInvalid)
	require.Empty(t, loaded)
}

func Test_Load_accepts_cron_descriptor(t *testing.T) {
	// Given
	setEnvironment(t, map[string]string{
		"TELEGRAM_BOT_TOKEN": "token",
		"TELEGRAM_CHAT_ID":   "-100123",
		"CRON_SCHEDULE":      "@every 6h",
	})

	// When
	loaded, err := config.Load()

	// Then
	require.NoError(t, err)
	require.Equal(t, "@every 6h", loaded.CronSpec())
}

func setEnvironment(t *testing.T, values map[string]string) {
	t.Helper()
	t.Setenv("ALIB_CATEGORIES", "tramka")
	t.Setenv("ALIB_SERIES", "")
	for key, value := range values {
		t.Setenv(key, value)
	}
}

func unsetEnvironment(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, os.Getenv(key))
	require.NoError(t, os.Unsetenv(key))
}
