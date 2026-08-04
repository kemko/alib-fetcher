package config_test

import (
	"testing"
	"time"

	"github.com/kemmko/alib-fetcher/internal/config"

	"github.com/stretchr/testify/require"
)

func Test_Load_applies_service_defaults(t *testing.T) {
	// Given
	setEnvironment(t, map[string]string{
		"TELEGRAM_BOT_TOKEN": "token",
		"TELEGRAM_CHAT_ID":   "-100123",
		"RUN_AT":             "",
		"TIMEZONE":           "",
		"STATE_PATH":         "",
		"ALIB_URL":           "",
		"TELEGRAM_API_BASE":  "",
		"HTTP_TIMEOUT":       "",
		"MESSAGE_LIMIT":      "",
	})

	// When
	loaded, err := config.Load()

	// Then
	require.NoError(t, err)
	require.Equal(t, "0 0 * * *", loaded.CronSpec())
	require.Equal(t, "Europe/Moscow", loaded.Location.String())
	require.Equal(t, "/tmp/alib-fetcher/state.db", loaded.StatePath)
	require.Equal(t, "https://www.alib.ru/tramka.phtml?tnew=7", loaded.AlibURL)
	require.Equal(t, "https://api.telegram.org", loaded.TelegramAPIBase)
	require.Equal(t, 30*time.Second, loaded.HTTPTimeout)
	require.Equal(t, 4000, loaded.MessageLimit)
}

func Test_Load_parses_custom_schedule(t *testing.T) {
	// Given
	setEnvironment(t, map[string]string{
		"TELEGRAM_BOT_TOKEN": "token",
		"TELEGRAM_CHAT_ID":   "@books",
		"RUN_AT":             "23:45",
		"TIMEZONE":           "Asia/Tbilisi",
		"STATE_PATH":         "/tmp/custom.db",
		"ALIB_URL":           "https://example.com/books",
		"TELEGRAM_API_BASE":  "https://telegram.example.test",
		"HTTP_TIMEOUT":       "15s",
		"MESSAGE_LIMIT":      "3500",
	})

	// When
	loaded, err := config.Load()

	// Then
	require.NoError(t, err)
	require.Equal(t, "45 23 * * *", loaded.CronSpec())
	require.Equal(t, "Asia/Tbilisi", loaded.Location.String())
	require.Equal(t, 15*time.Second, loaded.HTTPTimeout)
	require.Equal(t, 3500, loaded.MessageLimit)
}

func Test_Load_rejects_invalid_schedule(t *testing.T) {
	// Given
	setEnvironment(t, map[string]string{
		"TELEGRAM_BOT_TOKEN": "token",
		"TELEGRAM_CHAT_ID":   "chat",
		"RUN_AT":             "24:00",
	})

	// When
	loaded, err := config.Load()

	// Then
	require.ErrorIs(t, err, config.ErrInvalid)
	require.Empty(t, loaded)
}

func setEnvironment(t *testing.T, values map[string]string) {
	t.Helper()
	for key, value := range values {
		t.Setenv(key, value)
	}
}
