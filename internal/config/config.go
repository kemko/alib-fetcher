// Package config parses and validates process environment configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/robfig/cron/v3"
)

// ErrInvalid indicates that one or more environment values are unusable.
var ErrInvalid = errors.New("invalid configuration")

const (
	defaultAlibURL           = "https://www.alib.ru/tramka.phtml?tnew=7"
	defaultCronSchedule      = "0 0 * * *"
	defaultHTTPTimeout       = 30 * time.Second
	defaultMessageLimit      = 4000
	defaultRunOnStartup      = true
	defaultStatePath         = "/var/lib/alib-fetcher/state.db"
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
	cronSpec        string
	HTTPTimeout     time.Duration
	MessageLimit    int
	RunOnStartup    bool
}

// Load reads and validates process environment variables.
func Load() (Config, error) {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	chatID := os.Getenv("TELEGRAM_CHAT_ID")
	if token == "" || chatID == "" {
		return Config{}, fmt.Errorf("%w: TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID are required", ErrInvalid)
	}
	if !validTelegramChatID(chatID) {
		return Config{}, fmt.Errorf(
			"%w: TELEGRAM_CHAT_ID must be a signed decimal int64 or @channel username",
			ErrInvalid,
		)
	}

	cronSpec := valueOrDefault("CRON_SCHEDULE", defaultCronSchedule)
	if _, err := cron.ParseStandard(cronSpec); err != nil {
		return Config{}, fmt.Errorf("%w: CRON_SCHEDULE must be a valid cron expression: %w", ErrInvalid, err)
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
	runOnStartup, err := strconv.ParseBool(valueOrDefault("RUN_ON_STARTUP", strconv.FormatBool(defaultRunOnStartup)))
	if err != nil {
		return Config{}, fmt.Errorf("%w: RUN_ON_STARTUP must be a boolean", ErrInvalid)
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
		RunOnStartup:    runOnStartup,
		cronSpec:        cronSpec,
	}, nil
}

// CronSpec returns the validated cron schedule.
func (c Config) CronSpec() string {
	return c.cronSpec
}

func valueOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return fallback
}

func validTelegramChatID(chatID string) bool {
	if strings.HasPrefix(chatID, "@") {
		return len(chatID) > 1 && !strings.ContainsFunc(chatID, unicode.IsSpace)
	}

	_, err := strconv.ParseInt(chatID, 10, 64)

	return err == nil
}
