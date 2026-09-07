package config_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/config"
	"github.com/kemko/alib-fetcher/internal/store"

	"github.com/stretchr/testify/require"
)

func TestLoad_applies_defaults_and_preserves_explicit_zero_and_false(t *testing.T) {
	configPath := writeConfig(t, `alib_max_retries = 0
run_on_startup = false

[[chats]]
chat_id = "-100123"
telegram_token = "secret"
categories = ["tramka"]
`)

	loaded, err := config.Load(configPath)

	require.NoError(t, err)
	require.Equal(t, "/var/lib/alib-fetcher", loaded.StatePath)
	require.Equal(t, "0 0 * * *", loaded.CronSchedule)
	require.Equal(t, "Europe/Moscow", loaded.Location.String())
	require.Equal(t, 30*time.Second, loaded.HTTPTimeout)
	require.Equal(t, 32000, loaded.MessageLimit)
	require.Zero(t, loaded.AlibMaxRetries)
	require.False(t, loaded.RunOnStartup)
	require.Nil(t, loaded.FreshBooks)
	require.Equal(t, "-100123", loaded.Chats[0].ChatID)
	require.Equal(t, filepath.Join("/var/lib/alib-fetcher", "-100123.db"), loaded.Chats[0].StatePath)
}

func TestLoad_decodes_search_model_and_normalizes_recipients(t *testing.T) {
	configPath := writeConfig(t, `state_path = "data"
cron_schedule = "@every 6h"
timezone = "UTC"
http_timeout = "1s"
message_limit = 1000
fresh_books = "since:2021"

[[chats]]
chat_id = "+00123"
telegram_token = "token-one"
state_file = "old.db"
categories = ["tramka"]

[chats.filters]
seria = ["Литературные памятники", "ЖЗЛ"]
izdat = ["Наука"]

[[chats.queries]]
author = "Стругацкие"
cena2 = "3000"
fotoonly = "da"
`)

	loaded, err := config.Load(configPath)

	require.NoError(t, err)
	require.Equal(t, filepath.Join(filepath.Dir(configPath), "data"), loaded.StatePath)
	require.Equal(t, "123", loaded.Chats[0].ChatID)
	require.Equal(t, filepath.Join(loaded.StatePath, "old.db"), loaded.Chats[0].StatePath)
	require.Equal(t, []string{
		"https://www.alib.ru/tramka.phtml?tnew=7",
		"https://alib.ru/findp.php4?seria=%CB%E8%F2%E5%F0%E0%F2%F3%F0%ED%FB%E5+%EF%E0%EC%FF%F2%ED%E8%EA%E8&lday=7",
		"https://alib.ru/findp.php4?seria=%C6%C7%CB&lday=7",
		"https://alib.ru/findp.php4?izdat=%CD%E0%F3%EA%E0&lday=7",
		"https://alib.ru/findp.php4?author=%D1%F2%F0%F3%E3%E0%F6%EA%E8%E5&cena2=3000&fotoonly=da&lday=7",
	}, loaded.Chats[0].AlibURLs)
	require.Equal(t, 2021, loaded.FreshBooks.LowerYear(2026))
}

func TestLoad_rejects_unknown_fields_and_invalid_values_without_token(t *testing.T) {
	testCases := map[string]struct {
		body string
		want string
	}{
		"unknown top-level": {
			body: "unknown = true\n",
			want: "unknown",
		},
		"unknown chat field": {
			body: "[[chats]]\nchat_id = \"-1\"\ntelegram_token = \"secret-token\"\nwat = \"x\"\n",
			want: "wat",
		},
		"invalid chat": {
			body: "[[chats]]\nchat_id = \"not-a-chat\"\ntelegram_token = \"secret-token\"\ncategories = [\"tramka\"]\n",
			want: "chat_id",
		},
		"invalid search": {
			body: "[[chats]]\nchat_id = \"-1\"\ntelegram_token = \"secret-token\"\n\n[chats.filters]\nunknown = [\"x\"]\n",
			want: "unknown",
		},
		"wrong type": {
			body: "message_limit = \"large\"\n",
			want: "message_limit",
		},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			loaded, err := config.Load(writeConfig(t, testCase.body))

			require.ErrorIs(t, err, config.ErrInvalid)
			require.ErrorContains(t, err, testCase.want)
			require.NotContains(t, err.Error(), "secret-token")
			require.Empty(t, loaded)
		})
	}
}

func TestLoad_rejects_unsafe_and_colliding_state_files(t *testing.T) {
	testCases := map[string]string{
		"absolute": `[[chats]]
chat_id = "-1"
telegram_token = "a"
state_file = "/tmp/state.db"
categories = ["tramka"]
`,
		"nested": `[[chats]]
chat_id = "-1"
telegram_token = "a"
state_file = "nested/state.db"
categories = ["tramka"]
`,
		"same id": `[[chats]]
chat_id = "+1"
telegram_token = "a"
categories = ["tramka"]

[[chats]]
chat_id = "1"
telegram_token = "b"
categories = ["deti"]
`,
		"same file": `[[chats]]
chat_id = "-1"
telegram_token = "a"
state_file = "shared.db"
categories = ["tramka"]

[[chats]]
chat_id = "-2"
telegram_token = "b"
state_file = "shared.db"
categories = ["deti"]
`,
	}
	for name, body := range testCases {
		t.Run(name, func(t *testing.T) {
			loaded, err := config.Load(writeConfig(t, body))
			require.ErrorIs(t, err, config.ErrInvalid)
			require.Empty(t, loaded)
		})
	}
}

func TestLoad_ignores_legacy_environment(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "legacy-token")
	t.Setenv("TELEGRAM_CHAT_ID", "not-valid")
	t.Setenv("ALIB_CATEGORIES", "")

	loaded, err := config.Load(writeConfig(t, `[[chats]]
chat_id = "@Books"
telegram_token = "toml-token"
categories = ["tramka"]
`))

	require.NoError(t, err)
	require.Equal(t, "@books", loaded.Chats[0].ChatID)
	require.Equal(t, "toml-token", loaded.Chats[0].TelegramToken)
}

func TestLoadForMaintenance_does_not_require_service_settings(t *testing.T) {
	configPath := writeConfig(t, `state_path = "state"
[[chats]]
chat_id = "-100"
state_file = "legacy.db"
`)

	statePath, err := config.LoadForMaintenance(configPath, "-100")

	require.NoError(t, err)
	require.Equal(t, filepath.Join(filepath.Dir(configPath), "state", "legacy.db"), statePath)
}

func TestLoadForMaintenance_rejects_unknown_chat(t *testing.T) {
	configPath := writeConfig(t, `[[chats]]
chat_id = "-100"
`)

	_, err := config.LoadForMaintenance(configPath, "-101")

	require.ErrorIs(t, err, config.ErrInvalid)
}

func TestLoadForMaintenance_connects_existing_state_file_without_rewriting_history(t *testing.T) {
	stateDir := t.TempDir()
	statePath := filepath.Join(stateDir, "state.db")
	book := alib.Book{BuyURL: "https://example.com/old"}
	state, err := store.Open(statePath, time.Now())
	require.NoError(t, err)
	_, err = state.RecordDiscovered(context.Background(), []alib.Book{book}, time.Now())
	require.NoError(t, err)
	require.NoError(t, state.Close())
	configPath := writeConfig(t, "state_path = \""+stateDir+"\"\n\n[[chats]]\n"+
		"chat_id = \"-100\"\nstate_file = \"state.db\"\n")

	loadedPath, err := config.LoadForMaintenance(configPath, "-100")

	require.NoError(t, err)
	require.Equal(t, statePath, loadedPath)
	reopened, err := store.Open(loadedPath, time.Now())
	require.NoError(t, err)
	pending, err := reopened.Pending(context.Background())
	require.NoError(t, err)
	require.NoError(t, reopened.Close())
	require.Equal(t, []alib.Book{book}, pending)
}

func TestLoad_reads_repository_example(t *testing.T) {
	loaded, err := config.Load(filepath.Join("..", "..", "config.example.toml"))

	require.NoError(t, err)
	require.Len(t, loaded.Chats, 2)
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}
