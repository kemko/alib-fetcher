package config_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
	require.Zero(t, loaded.AlibDownloadDelay)
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

func TestLoad_validates_message_limit(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		limit int
		valid bool
	}{
		{limit: 63},
		{limit: 64, valid: true},
		{limit: 32768, valid: true},
		{limit: 32769},
	} {
		t.Run(strconv.Itoa(testCase.limit), func(t *testing.T) {
			configPath := writeConfig(t, fmt.Sprintf(`message_limit = %d
[[chats]]
chat_id = "-100123"
telegram_token = "token"
categories = ["tramka"]
`, testCase.limit))

			loaded, err := config.Load(configPath)

			if !testCase.valid {
				require.ErrorIs(t, err, config.ErrInvalid)
				require.ErrorContains(t, err, "message_limit")
				require.Empty(t, loaded)
				return
			}
			require.NoError(t, err)
			require.Equal(t, testCase.limit, loaded.MessageLimit)
		})
	}
}

func TestLoad_parses_alib_download_delay(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		value string
		want  time.Duration
	}{
		{value: `"0s"`},
		{value: `"0.5s"`, want: 500 * time.Millisecond},
		{value: `"500ms"`, want: 500 * time.Millisecond},
	} {
		t.Run(testCase.value, func(t *testing.T) {
			configPath := writeConfig(t, fmt.Sprintf(`alib_download_delay = %s
[[chats]]
chat_id = "-100123"
telegram_token = "token"
categories = ["tramka"]
`, testCase.value))

			loaded, err := config.Load(configPath)

			require.NoError(t, err)
			require.Equal(t, testCase.want, loaded.AlibDownloadDelay)
		})
	}
}

func TestLoad_rejects_invalid_service_values(t *testing.T) {
	t.Parallel()

	for field, values := range map[string][]string{
		"http_timeout":        {`"invalid"`, `"0s"`, `"-1s"`},
		"alib_max_retries":    {"-1"},
		"alib_download_delay": {`"invalid"`, `"-1s"`, "1", `"999999999999999999999999999h"`},
		"cron_schedule":       {`"not a cron expression"`},
		"fresh_books": {
			`"age:-1"`, `"age:+5"`, `"age:1.5"`, `"age:"`,
			`"since:999"`, `"since:0000"`, `"since:10000"`, `"since:20a1"`, `"fresh:2021"`,
		},
	} {
		for _, value := range values {
			t.Run(field+"="+value, func(t *testing.T) {
				configPath := writeConfig(t, fmt.Sprintf(`%s = %s
[[chats]]
chat_id = "-100123"
telegram_token = "secret-token"
categories = ["tramka"]
`, field, value))

				loaded, err := config.Load(configPath)

				require.ErrorIs(t, err, config.ErrInvalid)
				require.ErrorContains(t, err, field)
				require.NotContains(t, err.Error(), "secret-token")
				require.Empty(t, loaded)
			})
		}
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

func TestLoad_rejects_missing_equivalent_state_files_in_all_modes(t *testing.T) {
	t.Parallel()

	for name, files := range map[string][2]string{
		"case":          {"Books.db", "books.db"},
		"normalization": {"caf\u00e9.db", "cafe\u0301.db"},
		"both":          {"CAF\u00c9.db", "cafe\u0301.db"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// Given
			stateDir := t.TempDir()
			path := writeConfig(t, fmt.Sprintf(`state_path = %q
[[chats]]
chat_id = "-1"
telegram_token = "first"
state_file = %q
categories = ["tramka"]
[[chats]]
chat_id = "-2"
telegram_token = "second"
state_file = %q
categories = ["tramka"]
`, stateDir, files[0], files[1]))

			// When
			_, loadErr := config.Load(path)
			_, maintenanceErr := config.LoadForMaintenance(path, "-1")

			// Then
			require.ErrorIs(t, loadErr, config.ErrInvalid)
			require.ErrorContains(t, loadErr, "state_file collides")
			require.ErrorIs(t, maintenanceErr, config.ErrInvalid)
			require.ErrorContains(t, maintenanceErr, "state_file collides")
			entries, err := os.ReadDir(stateDir)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}

func TestLoad_rejects_existing_state_aliases_in_all_modes(t *testing.T) {
	t.Parallel()

	for name, link := range map[string]func(string, string) error{
		"hard link": os.Link,
		"symlink":   os.Symlink,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stateDir := t.TempDir()
			original := filepath.Join(stateDir, "original.db")
			require.NoError(t, os.WriteFile(original, []byte("existing state"), 0o600))
			require.NoError(t, link(original, filepath.Join(stateDir, "alias.db")))
			path := writeConfig(t, fmt.Sprintf(`state_path = %q
[[chats]]
chat_id = "-1"
telegram_token = "first"
state_file = "original.db"
categories = ["tramka"]
[[chats]]
chat_id = "-2"
telegram_token = "second"
state_file = "alias.db"
categories = ["tramka"]
`, stateDir))

			_, loadErr := config.Load(path)
			_, maintenanceErr := config.LoadForMaintenance(path, "-1")

			require.ErrorIs(t, loadErr, config.ErrInvalid)
			require.ErrorContains(t, loadErr, "state_file collides")
			require.ErrorIs(t, maintenanceErr, config.ErrInvalid)
			require.ErrorContains(t, maintenanceErr, "state_file collides")
			contents, err := os.ReadFile(original)
			require.NoError(t, err)
			require.Equal(t, "existing state", string(contents))
		})
	}
}

func TestLoad_rejects_dangling_state_symlinks_in_all_modes(t *testing.T) {
	t.Parallel()

	// Given
	stateDir := t.TempDir()
	shared := filepath.Join(stateDir, "shared.db")
	require.NoError(t, os.Symlink(shared, filepath.Join(stateDir, "first.db")))
	require.NoError(t, os.Symlink(shared, filepath.Join(stateDir, "second.db")))
	path := writeConfig(t, fmt.Sprintf(`state_path = %q
[[chats]]
chat_id = "-1"
telegram_token = "first"
state_file = "first.db"
categories = ["tramka"]
[[chats]]
chat_id = "-2"
telegram_token = "second"
state_file = "second.db"
categories = ["tramka"]
`, stateDir))

	// When
	_, loadErr := config.Load(path)
	_, maintenanceErr := config.LoadForMaintenance(path, "-1")

	// Then
	require.ErrorIs(t, loadErr, config.ErrInvalid)
	require.ErrorContains(t, loadErr, "state_file")
	require.ErrorIs(t, maintenanceErr, config.ErrInvalid)
	require.ErrorContains(t, maintenanceErr, "state_file")
	require.NoFileExists(t, shared)
}

func TestLoad_reports_the_same_error_for_unchanged_invalid_config(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"global keys": "unknown_a = true\nunknown_b = true\nunknown_c = true\n",
		"chat keys":   "[[chats]]\nunknown_a = true\nunknown_b = true\nunknown_c = true\n",
		"query keys": `[[chats]]
chat_id = "-1"
telegram_token = "secret"
[[chats.queries]]
unknown_a = "a"
unknown_b = "b"
unknown_c = "c"
`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := writeConfig(t, body)
			_, firstErr := config.Load(path)
			require.Error(t, firstErr)
			for range 100 {
				_, err := config.Load(path)
				require.EqualError(t, err, firstErr.Error())
			}
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
	require.Equal(t, 500*time.Millisecond, loaded.AlibDownloadDelay)
	require.Equal(t, "/var/lib/alib-fetcher/state.db", loaded.Chats[0].StatePath)
	require.Equal(t, "/var/lib/alib-fetcher/@another_channel.db", loaded.Chats[1].StatePath)
	require.Equal(t, []string{
		"https://www.alib.ru/tramka.phtml?tnew=7",
		"https://www.alib.ru/detektivy.phtml?tnew=7",
		"https://alib.ru/findp.php4?seria=%CB%E8%F2%E5%F0%E0%F2%F3%F0%ED%FB%E5+%EF%E0%EC%FF%F2%ED%E8%EA%E8&lday=7",
		"https://alib.ru/findp.php4?seria=%C6%C7%CB&lday=7",
		"https://alib.ru/findp.php4?izdat=%CD%E0%F3%EA%E0&lday=7",
		"https://alib.ru/findp.php4?author=%D1%F2%F0%F3%E3%E0%F6%EA%E8%E5&seria=%C1%E8%E1%EB%E8%EE%F2%E5%EA%E0+%EF%F0%E8%EA%EB%FE%F7%E5%ED%E8%E9&cena2=3000&lday=14&fotoonly=da",
	}, loaded.Chats[0].AlibURLs)
	require.Equal(t, "REPLACE_WITH_TOKEN", loaded.Chats[0].TelegramToken)
	require.Equal(t, "REPLACE_WITH_ANOTHER_TOKEN", loaded.Chats[1].TelegramToken)
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}
