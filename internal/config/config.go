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

var errInvalidFreshBooks = errors.New("must use age:N with a non-negative integer or since:YYYY")

const (
	defaultAlibURL             = "https://www.alib.ru/tramka.phtml?tnew=7"
	defaultAlibRequestInterval = time.Second
	defaultCronSchedule        = "0 0 * * *"
	defaultHTTPTimeout         = 30 * time.Second
	defaultMessageLimit        = 32000
	defaultRunOnStartup        = true
	defaultStatePath           = "/var/lib/alib-fetcher/state.db"
	defaultTelegramAPIBase     = "https://api.telegram.org"
	defaultTimezone            = "Europe/Moscow"
	telegramHardMessageLimit   = 32768
)

type freshBooksMode uint8

const (
	freshBooksAge freshBooksMode = iota
	freshBooksSince
)

// FreshBooksPolicy describes an optional inclusive publication-year threshold.
type FreshBooksPolicy struct {
	mode  freshBooksMode
	value int
}

// LowerYear returns the inclusive lower publication year for currentYear.
func (policy FreshBooksPolicy) LowerYear(currentYear int) int {
	if policy.mode == freshBooksAge {
		return currentYear - policy.value
	}

	return policy.value
}

// Config contains validated process configuration.
type Config struct {
	Location            *time.Location
	FreshBooks          *FreshBooksPolicy
	TelegramToken       string
	TelegramChatID      string
	TelegramAPIBase     string
	AlibURL             string
	StatePath           string
	cronSpec            string
	AlibRequestInterval time.Duration
	HTTPTimeout         time.Duration
	MessageLimit        int
	RunOnStartup        bool
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

	settings := Config{
		TelegramToken:   token,
		TelegramChatID:  chatID,
		TelegramAPIBase: valueOrDefault("TELEGRAM_API_BASE", defaultTelegramAPIBase),
		AlibURL:         valueOrDefault("ALIB_URL", defaultAlibURL),
		StatePath:       LoadStatePath(),
	}

	return loadValidatedConfig(settings)
}

func loadValidatedConfig(settings Config) (Config, error) {
	settings.cronSpec = valueOrDefault("CRON_SCHEDULE", defaultCronSchedule)
	if _, err := cron.ParseStandard(settings.cronSpec); err != nil {
		return Config{}, fmt.Errorf("%w: CRON_SCHEDULE must be a valid cron expression: %w", ErrInvalid, err)
	}

	location, err := time.LoadLocation(valueOrDefault("TIMEZONE", defaultTimezone))
	if err != nil {
		return Config{}, fmt.Errorf("%w: load TIMEZONE: %w", ErrInvalid, err)
	}
	settings.Location = location
	settings.HTTPTimeout, err = parsePositiveDuration("HTTP_TIMEOUT", defaultHTTPTimeout)
	if err != nil {
		return Config{}, err
	}
	settings.AlibRequestInterval, err = parseNonNegativeDuration(
		"ALIB_REQUEST_INTERVAL",
		defaultAlibRequestInterval,
	)
	if err != nil {
		return Config{}, err
	}
	settings.MessageLimit, err = parseMessageLimit()
	if err != nil {
		return Config{}, err
	}
	settings.RunOnStartup, err = parseRunOnStartup()
	if err != nil {
		return Config{}, err
	}
	if value := os.Getenv("FRESH_BOOKS"); value != "" {
		policy, parseErr := parseFreshBooks(value)
		if parseErr != nil {
			return Config{}, fmt.Errorf("%w: FRESH_BOOKS %w", ErrInvalid, parseErr)
		}
		settings.FreshBooks = &policy
	}

	return settings, nil
}

func parsePositiveDuration(name string, defaultValue time.Duration) (time.Duration, error) {
	value, err := time.ParseDuration(valueOrDefault(name, defaultValue.String()))
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%w: %s must be a positive Go duration", ErrInvalid, name)
	}

	return value, nil
}

func parseNonNegativeDuration(name string, defaultValue time.Duration) (time.Duration, error) {
	value, err := time.ParseDuration(valueOrDefault(name, defaultValue.String()))
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%w: %s must be a non-negative Go duration", ErrInvalid, name)
	}

	return value, nil
}

func parseMessageLimit() (int, error) {
	value, err := strconv.Atoi(valueOrDefault("MESSAGE_LIMIT", strconv.Itoa(defaultMessageLimit)))
	if err != nil || value < 64 || value > telegramHardMessageLimit {
		return 0, fmt.Errorf("%w: MESSAGE_LIMIT must be between 64 and %d", ErrInvalid, telegramHardMessageLimit)
	}

	return value, nil
}

func parseRunOnStartup() (bool, error) {
	value, err := strconv.ParseBool(valueOrDefault("RUN_ON_STARTUP", strconv.FormatBool(defaultRunOnStartup)))
	if err != nil {
		return false, fmt.Errorf("%w: RUN_ON_STARTUP must be a boolean", ErrInvalid)
	}

	return value, nil
}

// LoadStatePath reads the state database path without validating the rest of the
// service configuration.
func LoadStatePath() string {
	return valueOrDefault("STATE_PATH", defaultStatePath)
}

func parseFreshBooks(value string) (FreshBooksPolicy, error) {
	mode, argument, found := strings.Cut(value, ":")
	if !found || !containsOnlyDigits(argument) {
		return FreshBooksPolicy{}, errInvalidFreshBooks
	}

	parsed, err := strconv.Atoi(argument)
	if err != nil {
		return FreshBooksPolicy{}, errInvalidFreshBooks
	}

	switch mode {
	case "age":
		return FreshBooksPolicy{mode: freshBooksAge, value: parsed}, nil
	case "since":
		if len(argument) != 4 || parsed < 1000 {
			return FreshBooksPolicy{}, errInvalidFreshBooks
		}

		return FreshBooksPolicy{mode: freshBooksSince, value: parsed}, nil
	default:
		return FreshBooksPolicy{}, errInvalidFreshBooks
	}
}

func containsOnlyDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}

	return true
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
