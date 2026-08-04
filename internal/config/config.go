// Package config parses and validates process environment configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// ErrInvalid indicates that one or more environment values are unusable.
var ErrInvalid = errors.New("invalid configuration")

const (
	defaultAlibURL           = "https://www.alib.ru/tramka.phtml?tnew=7"
	defaultHTTPTimeout       = 30 * time.Second
	defaultMessageLimit      = 4000
	defaultRunAt             = "00:00"
	defaultStatePath         = "/tmp/alib-fetcher/state.db"
	defaultTelegramAPIBase   = "https://api.telegram.org"
	defaultTimezone          = "Europe/Moscow"
	telegramHardMessageLimit = 4096
)

// Config contains validated process configuration.
type Config struct {
	Location        *time.Location
	TelegramToken   string
	TelegramChatID  string
	TelegramAPIBase string
	AlibURL         string
	StatePath       string
	HTTPTimeout     time.Duration
	MessageLimit    int
	hour            int
	minute          int
}

// Load reads and validates process environment variables.
func Load() (Config, error) {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	chatID := os.Getenv("TELEGRAM_CHAT_ID")
	if token == "" || chatID == "" {
		return Config{}, fmt.Errorf("%w: TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID are required", ErrInvalid)
	}

	runAt, err := time.Parse("15:04", valueOrDefault("RUN_AT", defaultRunAt))
	if err != nil {
		return Config{}, fmt.Errorf("%w: RUN_AT must use HH:MM: %w", ErrInvalid, err)
	}
	location, err := time.LoadLocation(valueOrDefault("TIMEZONE", defaultTimezone))
	if err != nil {
		return Config{}, fmt.Errorf("%w: load TIMEZONE: %w", ErrInvalid, err)
	}
	timeout, err := time.ParseDuration(valueOrDefault("HTTP_TIMEOUT", defaultHTTPTimeout.String()))
	if err != nil || timeout <= 0 {
		return Config{}, fmt.Errorf("%w: HTTP_TIMEOUT must be a positive Go duration", ErrInvalid)
	}
	messageLimit, err := strconv.Atoi(valueOrDefault("MESSAGE_LIMIT", strconv.Itoa(defaultMessageLimit)))
	if err != nil || messageLimit < 64 || messageLimit > telegramHardMessageLimit {
		return Config{}, fmt.Errorf("%w: MESSAGE_LIMIT must be between 64 and %d", ErrInvalid, telegramHardMessageLimit)
	}

	return Config{
		TelegramToken:   token,
		TelegramChatID:  chatID,
		TelegramAPIBase: valueOrDefault("TELEGRAM_API_BASE", defaultTelegramAPIBase),
		AlibURL:         valueOrDefault("ALIB_URL", defaultAlibURL),
		StatePath:       valueOrDefault("STATE_PATH", defaultStatePath),
		Location:        location,
		HTTPTimeout:     timeout,
		MessageLimit:    messageLimit,
		hour:            runAt.Hour(),
		minute:          runAt.Minute(),
	}, nil
}

// CronSpec returns a five-field daily cron expression.
func (c Config) CronSpec() string {
	return fmt.Sprintf("%d %d * * *", c.minute, c.hour)
}

func valueOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return fallback
}
