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
		"TELEGRAM_BOT_TOKEN": "token",
		"TELEGRAM_CHAT_ID":   "-100123",
		"CRON_SCHEDULE":      "",
		"TIMEZONE":           "",
		"STATE_PATH":         "",
		"ALIB_URL":           "",
		"TELEGRAM_API_BASE":  "",
		"HTTP_TIMEOUT":       "",
		"MESSAGE_LIMIT":      "",
		"RUN_ON_STARTUP":     "",
		"FRESH_BOOKS":        "",
	})

	// When
	loaded, err := config.Load()

	// Then
	require.NoError(t, err)
	require.Equal(t, "0 0 * * *", loaded.CronSpec())
	require.Equal(t, "Europe/Moscow", loaded.Location.String())
	require.Equal(t, "/var/lib/alib-fetcher/state.db", loaded.StatePath)
	require.Equal(t, "https://www.alib.ru/tramka.phtml?tnew=7", loaded.AlibURL)
	require.Equal(t, "https://api.telegram.org", loaded.TelegramAPIBase)
	require.Equal(t, 30*time.Second, loaded.HTTPTimeout)
	require.Equal(t, 4000, loaded.MessageLimit)
	require.True(t, loaded.RunOnStartup)
	require.Nil(t, loaded.FreshBooks)
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
		"TELEGRAM_BOT_TOKEN": "token",
		"TELEGRAM_CHAT_ID":   "@books",
		"CRON_SCHEDULE":      "*/15 8-18 * * 1-5",
		"TIMEZONE":           "Asia/Tbilisi",
		"STATE_PATH":         "/tmp/custom.db",
		"ALIB_URL":           "https://example.com/books",
		"TELEGRAM_API_BASE":  "https://telegram.example.test",
		"HTTP_TIMEOUT":       "15s",
		"MESSAGE_LIMIT":      "3500",
		"RUN_ON_STARTUP":     "false",
	})

	// When
	loaded, err := config.Load()

	// Then
	require.NoError(t, err)
	require.Equal(t, "*/15 8-18 * * 1-5", loaded.CronSpec())
	require.Equal(t, "Asia/Tbilisi", loaded.Location.String())
	require.Equal(t, 15*time.Second, loaded.HTTPTimeout)
	require.Equal(t, 3500, loaded.MessageLimit)
	require.False(t, loaded.RunOnStartup)
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
	for key, value := range values {
		t.Setenv(key, value)
	}
}

func unsetEnvironment(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, os.Getenv(key))
	require.NoError(t, os.Unsetenv(key))
}
