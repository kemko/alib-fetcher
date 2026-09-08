// Package config parses and validates the TOML process configuration.
package config

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/pelletier/go-toml/v2"
	"github.com/robfig/cron/v3"
	"golang.org/x/text/unicode/norm"
)

// ErrInvalid indicates that one or more configuration values are unusable.
var ErrInvalid = errors.New("invalid configuration")

var errInvalidFreshBooks = errors.New("must use age:N with a non-negative integer or since:YYYY")

const (
	defaultAlibMaxRetries    = 3
	defaultCronSchedule      = "0 0 * * *"
	defaultHTTPTimeout       = 30 * time.Second
	defaultMessageLimit      = 32000
	defaultRunOnStartup      = true
	defaultStatePath         = "/var/lib/alib-fetcher"
	defaultTimezone          = "Europe/Moscow"
	telegramHardMessageLimit = 32768
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

// Config contains validated process-level settings and recipients.
type Config struct {
	Location          *time.Location
	FreshBooks        *FreshBooksPolicy
	StatePath         string
	CronSchedule      string
	Path              string
	Chats             []Chat
	AlibDownloadDelay time.Duration
	AlibMaxRetries    int
	HTTPTimeout       time.Duration
	MessageLimit      int
	RunOnStartup      bool
}

// Chat contains settings and generated sources for one recipient.
type Chat struct {
	ChatID        string
	TelegramToken string
	StateFile     string
	StatePath     string
	Search        SearchConfig
	AlibURLs      []string
}

type rawConfig struct {
	StatePath         *string   `toml:"state_path"`
	CronSchedule      *string   `toml:"cron_schedule"`
	Timezone          *string   `toml:"timezone"`
	RunOnStartup      *bool     `toml:"run_on_startup"`
	HTTPTimeout       *string   `toml:"http_timeout"`
	AlibDownloadDelay *string   `toml:"alib_download_delay"`
	AlibMaxRetries    *int      `toml:"alib_max_retries"`
	MessageLimit      *int      `toml:"message_limit"`
	FreshBooks        *string   `toml:"fresh_books"`
	Chats             []rawChat `toml:"chats"`
}

type rawChat struct {
	ChatID        string              `toml:"chat_id"`
	TelegramToken string              `toml:"telegram_token"`
	StateFile     *string             `toml:"state_file"`
	Categories    []string            `toml:"categories"`
	Filters       map[string][]string `toml:"filters"`
	Queries       []map[string]string `toml:"queries"`
}

// Load reads a TOML configuration. With no argument it reads ./config.toml.
func Load(paths ...string) (Config, error) {
	raw, absolutePath, err := readRaw(paths...)
	if err != nil {
		return Config{}, err
	}

	return validate(raw, absolutePath)
}

// LoadForMaintenance reads only the state path and recipient state mapping.
// It intentionally does not require tokens, sources, or service settings.
func LoadForMaintenance(path, chatID string) (string, error) {
	raw, configPath, err := readRaw(path)
	if err != nil {
		return "", err
	}
	stateDir, err := resolveStateDir(raw.StatePath, configPath)
	if err != nil {
		return "", err
	}
	chats, err := validateStateMappings(raw.Chats, stateDir)
	if err != nil {
		return "", err
	}
	normalized, err := normalizeChatID(chatID)
	if err != nil {
		return "", fmt.Errorf("%w: chat_id %w", ErrInvalid, err)
	}
	for _, chat := range chats {
		if chat.ChatID == normalized {
			return chat.StatePath, nil
		}
	}
	return "", fmt.Errorf("%w: unknown chat %q", ErrInvalid, normalized)
}

// NormalizeChatID returns the canonical representation used in configuration.
func NormalizeChatID(value string) (string, error) { return normalizeChatID(value) }

func validate(raw rawConfig, configPath string) (Config, error) {
	settings := Config{
		StatePath:      stringValue(raw.StatePath, defaultStatePath),
		CronSchedule:   stringValue(raw.CronSchedule, defaultCronSchedule),
		AlibMaxRetries: intValue(raw.AlibMaxRetries, defaultAlibMaxRetries),
		MessageLimit:   intValue(raw.MessageLimit, defaultMessageLimit),
		RunOnStartup:   boolValue(raw.RunOnStartup, defaultRunOnStartup),
		Path:           configPath,
	}
	if _, err := cron.ParseStandard(settings.CronSchedule); err != nil {
		return Config{}, fmt.Errorf("%w: cron_schedule must be a valid cron expression: %w", ErrInvalid, err)
	}
	if settings.AlibMaxRetries < 0 {
		return Config{}, fmt.Errorf("%w: alib_max_retries must be a non-negative integer", ErrInvalid)
	}
	if settings.MessageLimit < 64 || settings.MessageLimit > telegramHardMessageLimit {
		return Config{}, fmt.Errorf("%w: message_limit must be between 64 and %d", ErrInvalid, telegramHardMessageLimit)
	}
	var err error
	settings.Location, err = time.LoadLocation(stringValue(raw.Timezone, defaultTimezone))
	if err != nil {
		return Config{}, fmt.Errorf("%w: load timezone: %w", ErrInvalid, err)
	}
	settings.HTTPTimeout, err = parseDuration(raw.HTTPTimeout)
	if err != nil {
		return Config{}, fmt.Errorf("%w: http_timeout must be a positive Go duration", ErrInvalid)
	}
	settings.AlibDownloadDelay, err = parseNonNegativeDuration(raw.AlibDownloadDelay)
	if err != nil {
		return Config{}, fmt.Errorf("%w: alib_download_delay must be a non-negative Go duration", ErrInvalid)
	}
	if raw.FreshBooks != nil && *raw.FreshBooks != "" {
		policy, parseErr := parseFreshBooks(*raw.FreshBooks)
		if parseErr != nil {
			return Config{}, fmt.Errorf("%w: fresh_books %w", ErrInvalid, parseErr)
		}
		settings.FreshBooks = &policy
	}

	stateDir, err := resolveStateDir(raw.StatePath, configPath)
	if err != nil {
		return Config{}, err
	}
	settings.StatePath = stateDir
	settings.Chats, err = validateChats(raw.Chats, settings.StatePath)
	if err != nil {
		return Config{}, err
	}
	return settings, nil
}

func readRaw(paths ...string) (rawConfig, string, error) {
	path := "./config.toml"
	if len(paths) > 1 {
		return rawConfig{}, "", fmt.Errorf("%w: config path specified more than once", ErrInvalid)
	}
	if len(paths) == 1 && paths[0] != "" {
		path = paths[0]
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return rawConfig{}, "", fmt.Errorf("%w: resolve config path: %w", ErrInvalid, err)
	}
	contents, err := os.ReadFile(absolutePath) //nolint:gosec // path is explicitly supplied by the operator
	if err != nil {
		return rawConfig{}, "", fmt.Errorf("%w: read config: %w", ErrInvalid, err)
	}
	var raw rawConfig
	if decodeErr := toml.Unmarshal(contents, &raw); decodeErr != nil {
		return rawConfig{}, "", fmt.Errorf("%w: %w", ErrInvalid, formatDecodeError(decodeErr))
	}
	var document map[string]any
	if decodeErr := toml.Unmarshal(contents, &document); decodeErr != nil {
		return rawConfig{}, "", fmt.Errorf("%w: %w", ErrInvalid, formatDecodeError(decodeErr))
	}
	if validationErr := validateDocumentKeys(document); validationErr != nil {
		return rawConfig{}, "", fmt.Errorf("%w: %w", ErrInvalid, validationErr)
	}
	return raw, absolutePath, nil
}

func formatDecodeError(err error) error {
	var decodeErr *toml.DecodeError
	if errors.As(err, &decodeErr) && len(decodeErr.Key()) > 0 {
		return fmt.Errorf("decode config field %s: %w", strings.Join(decodeErr.Key(), "."), err)
	}
	return fmt.Errorf("decode config: %w", err)
}

func validateDocumentKeys(document map[string]any) error {
	for _, key := range slices.Sorted(maps.Keys(document)) {
		if !oneOf(key, "state_path", "cron_schedule", "timezone", "run_on_startup", "http_timeout",
			"alib_max_retries", "alib_download_delay", "message_limit", "fresh_books", "chats") {
			return fmt.Errorf("unknown field %q", key)
		}
	}
	chats, ok := document["chats"]
	if !ok {
		return nil
	}
	for index, item := range asAnySlice(chats) {
		chat, chatOK := item.(map[string]any)
		if !chatOK {
			continue
		}
		for _, key := range slices.Sorted(maps.Keys(chat)) {
			value := chat[key]
			if !oneOf(key, "chat_id", "telegram_token", "state_file", "categories", "filters", "queries") {
				return fmt.Errorf("unknown field %q in chats[%d]", key, index)
			}
			if key == "filters" {
				if _, filterOK := value.(map[string]any); !filterOK {
					return fmt.Errorf("filters in chats[%d] must be a table", index)
				}
			}
		}
	}
	return nil
}

func asAnySlice(value any) []any {
	slice, ok := value.([]any)
	if !ok {
		return nil
	}
	return slice
}

func resolveStateDir(value *string, configPath string) (string, error) {
	stateDir := stringValue(value, defaultStatePath)
	if !filepath.IsAbs(stateDir) {
		stateDir = filepath.Join(filepath.Dir(configPath), stateDir)
	}
	abs, err := filepath.Abs(stateDir)
	if err != nil {
		return "", fmt.Errorf("%w: resolve state_path: %w", ErrInvalid, err)
	}
	return filepath.Clean(abs), nil
}

func validateStateMappings(rawChats []rawChat, stateDir string) ([]Chat, error) {
	if len(rawChats) == 0 {
		return nil, fmt.Errorf("%w: chats must contain at least one recipient", ErrInvalid)
	}
	chats := make([]Chat, 0, len(rawChats))
	seenIDs := make(map[string]struct{}, len(rawChats))
	seenFiles := make(map[string]os.FileInfo, len(rawChats))
	for index, raw := range rawChats {
		prefix := fmt.Sprintf("chats[%d]", index)
		chatID, err := normalizeChatID(raw.ChatID)
		if err != nil {
			return nil, fmt.Errorf("%w: %s.chat_id %w", ErrInvalid, prefix, err)
		}
		if _, exists := seenIDs[chatID]; exists {
			return nil, fmt.Errorf("%w: %s.chat_id duplicates a normalized recipient", ErrInvalid, prefix)
		}
		seenIDs[chatID] = struct{}{}
		stateFile := chatID + ".db"
		if raw.StateFile != nil {
			stateFile = *raw.StateFile
		}
		if stateFileErr := validateStateFile(stateFile); stateFileErr != nil {
			return nil, fmt.Errorf("%w: %s.state_file %w", ErrInvalid, prefix, stateFileErr)
		}
		statePath := filepath.Join(stateDir, stateFile)
		if aliasErr := validateStateAlias(statePath, seenFiles); aliasErr != nil {
			return nil, fmt.Errorf("%w: %s.state_file %w", ErrInvalid, prefix, aliasErr)
		}
		chats = append(chats, Chat{ChatID: chatID, StateFile: stateFile, StatePath: statePath})
	}
	return chats, nil
}

func validateStateAlias(path string, seen map[string]os.FileInfo) error {
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect state file: %w", err)
	}
	if info != nil && info.Mode()&os.ModeSymlink != 0 {
		info, err = os.Stat(path)
		if err != nil {
			return fmt.Errorf("resolve state symlink: %w", err)
		}
	}
	// Use portable comparison keys without changing the configured database paths.
	key := norm.NFC.String(path)
	for previousKey, previousInfo := range seen {
		if strings.EqualFold(key, previousKey) ||
			(info != nil && previousInfo != nil && os.SameFile(info, previousInfo)) {
			return errors.New("collides with another recipient")
		}
	}
	seen[key] = info
	return nil
}

func validateChats(rawChats []rawChat, stateDir string) ([]Chat, error) {
	chats, err := validateStateMappings(rawChats, stateDir)
	if err != nil {
		return nil, err
	}
	for index, raw := range rawChats {
		prefix := fmt.Sprintf("chats[%d]", index)
		if raw.TelegramToken == "" {
			return nil, fmt.Errorf("%w: %s.telegram_token is required", ErrInvalid, prefix)
		}

		search := SearchConfig{
			Categories: append([]string(nil), raw.Categories...),
			Filters:    cloneFilters(raw.Filters),
			Queries:    cloneQueries(raw.Queries),
		}
		urls, searchErr := buildSearchURLs(search.Categories, search.Filters, search.Queries, searchErrorContext{
			fields: map[string]string{"categories": prefix + ".categories"},
		})
		if searchErr != nil {
			return nil, fmt.Errorf("%w: %s.search: %w", ErrInvalid, prefix, searchErr)
		}
		chats[index].TelegramToken = raw.TelegramToken
		chats[index].Search = search
		chats[index].AlibURLs = urls
	}
	return chats, nil
}

func normalizeChatID(value string) (string, error) {
	if strings.HasPrefix(value, "@") {
		if len(value) == 1 || strings.ContainsFunc(value, unicode.IsSpace) {
			return "", errors.New("must be a signed decimal int64 or @channel username")
		}
		return strings.ToLower(value), nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return "", errors.New("must be a signed decimal int64 or @channel username")
	}
	return strconv.FormatInt(parsed, 10), nil
}

func validateStateFile(value string) error {
	if value == "" || value == "." || value == ".." || strings.ContainsRune(value, '\x00') ||
		strings.ContainsAny(value, `/\\`) || filepath.IsAbs(value) {
		return errors.New("must be a safe file name")
	}
	return nil
}

func parseDuration(value *string) (time.Duration, error) {
	if value == nil {
		return defaultHTTPTimeout, nil
	}
	parsed, err := time.ParseDuration(*value)
	if err != nil || parsed <= 0 {
		return 0, errors.New("invalid duration")
	}
	return parsed, nil
}

func parseNonNegativeDuration(value *string) (time.Duration, error) {
	if value == nil {
		return 0, nil
	}
	parsed, err := time.ParseDuration(*value)
	if err != nil || parsed < 0 {
		return 0, errors.New("invalid duration")
	}
	return parsed, nil
}

func stringValue(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}

func intValue(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func boolValue(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func cloneFilters(filters map[string][]string) map[string][]string {
	if filters == nil {
		return nil
	}
	cloned := make(map[string][]string, len(filters))
	for key, values := range filters {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

func cloneQueries(queries []map[string]string) []map[string]string {
	if queries == nil {
		return nil
	}
	cloned := make([]map[string]string, len(queries))
	for index, query := range queries {
		cloned[index] = make(map[string]string, len(query))
		for key, value := range query {
			cloned[index][key] = value
		}
	}
	return cloned
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

func isASCIIWord(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') {
			return false
		}
	}
	return true
}
