package config_test

import (
	"math"
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
