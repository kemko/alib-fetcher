package process

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/app"
	"github.com/kemko/alib-fetcher/internal/store"
	"github.com/kemko/alib-fetcher/internal/telegram"
)

func Test_RunRecipients_once_runs_all_recipients_and_joins_errors(t *testing.T) {
	t.Parallel()

	// Given
	var firstCalls atomic.Int32
	var secondCalls atomic.Int32
	firstErr := errors.New("first recipient failed")
	recipients := []Recipient{
		{
			ChatID:    "-1001",
			StatePath: filepath.Join(t.TempDir(), "first.db"),
			Dependencies: app.Dependencies{
				Fetcher:      countedErrorFetcher{calls: &firstCalls, err: firstErr},
				Sender:       noopSender{},
				MessageLimit: 4096,
				Now:          time.Now,
			},
		},
		{
			ChatID:    "-1002",
			StatePath: filepath.Join(t.TempDir(), "second.db"),
			Dependencies: app.Dependencies{
				Fetcher:      countingFetcher{calls: &secondCalls},
				Sender:       noopSender{},
				MessageLimit: 4096,
				Now:          time.Now,
			},
		},
	}

	// When
	err := RunRecipients(context.Background(), Settings{}, recipients, true, slog.New(slog.DiscardHandler))

	// Then
	require.ErrorIs(t, err, firstErr)
	require.Equal(t, int32(1), firstCalls.Load())
	require.Equal(t, int32(1), secondCalls.Load())
}

func Test_RunRecipients_keeps_common_buy_url_and_delivery_state_independent(t *testing.T) {
	t.Parallel()

	// Given
	book := alib.Book{Title: "Одна книга", BuyURL: "https://example.com/book"}
	statePaths := []string{
		filepath.Join(t.TempDir(), "first.db"),
		filepath.Join(t.TempDir(), "second.db"),
	}
	senders := []*recordingSender{{}, {}}
	recipients := make([]Recipient, 2)
	for index := range recipients {
		recipients[index] = Recipient{
			ChatID:    "-100" + strconv.Itoa(index+1),
			StatePath: statePaths[index],
			Dependencies: app.Dependencies{
				Fetcher:      bookFetcher{books: []alib.Book{book}},
				Sender:       senders[index],
				MessageLimit: 4096,
				Now:          time.Now,
			},
		}
	}

	// When
	err := RunRecipients(context.Background(), Settings{}, recipients, true, slog.New(slog.DiscardHandler))

	// Then
	require.NoError(t, err)
	for index, path := range statePaths {
		require.Len(t, senders[index].messages, 1)
		state, openErr := store.Open(path, time.Now())
		require.NoError(t, openErr)
		pending, pendingErr := state.Pending(context.Background())
		require.NoError(t, pendingErr)
		require.NoError(t, state.Close())
		require.Empty(t, pending)
	}
}

func Test_handleGroupedCallback_routes_shared_client_and_rejects_foreign_or_ambiguous_chats(t *testing.T) {
	t.Parallel()

	// Given
	client := &recordingCallbackClient{}
	firstSender := &recordingSender{}
	secondSender := &recordingSender{}
	firstRunner := newDigestRunnerForRecipient(Recipient{
		ChatID:    "-1001",
		StatePath: filepath.Join(t.TempDir(), "first.db"),
		Dependencies: app.Dependencies{
			Fetcher:      emptyFetcher{},
			Sender:       firstSender,
			MessageLimit: 4096,
			Now:          time.Now,
		},
	}, slog.New(slog.DiscardHandler))
	secondRunner := newDigestRunnerForRecipient(Recipient{
		ChatID:    "-1002",
		StatePath: filepath.Join(t.TempDir(), "second.db"),
		Dependencies: app.Dependencies{
			Fetcher:      emptyFetcher{},
			Sender:       secondSender,
			MessageLimit: 4096,
			Now:          time.Now,
		},
	}, slog.New(slog.DiscardHandler))
	group := callbackGroup{
		client: client,
		recipients: []callbackRecipient{
			{chatID: "-1001", runner: firstRunner},
			{chatID: "-1002", runner: secondRunner},
		},
	}

	// When
	handleGroupedCallback(context.Background(), group, telegram.Callback{
		ID:            "first",
		Data:          telegram.RefreshCallbackData,
		MessageChatID: -1001,
		MessageID:     1,
	}, slog.New(slog.DiscardHandler))
	firstRunner.wait()
	handleGroupedCallback(context.Background(), group, telegram.Callback{
		ID:            "foreign",
		Data:          telegram.RefreshCallbackData,
		MessageChatID: -1003,
		MessageID:     2,
	}, slog.New(slog.DiscardHandler))
	ambiguous := callbackGroup{
		client: client,
		recipients: []callbackRecipient{
			{chatID: "@books", runner: firstRunner},
			{chatID: "@BOOKS", runner: secondRunner},
		},
	}
	handleGroupedCallback(context.Background(), ambiguous, telegram.Callback{
		ID:                  "ambiguous",
		Data:                telegram.RefreshCallbackData,
		MessageChatUsername: "books",
		MessageID:           3,
	}, slog.New(slog.DiscardHandler))

	// Then
	require.Len(t, firstSender.messages, 1)
	require.Empty(t, secondSender.messages)
	require.Equal(t, []callbackAnswer{
		{id: "first", text: refreshStartedText},
		{id: "foreign", text: refreshUnavailableText},
		{id: "ambiguous", text: refreshUnavailableText},
	}, client.answersSnapshot())
}

func Test_RunRecipients_service_uses_one_listener_for_shared_client_and_stops_all_work(t *testing.T) {
	t.Parallel()

	// Given
	ctx, cancel := context.WithCancel(context.Background())
	client := &recordingCallbackClient{cancel: cancel}
	recipients := []Recipient{
		newEmptyRecipient(t, "-1001", client),
		newEmptyRecipient(t, "-1002", client),
	}
	done := make(chan error, 1)

	// When
	go func() {
		done <- RunRecipients(ctx, Settings{
			Location:     time.UTC,
			CronSpec:     "@every 1h",
			RunOnStartup: false,
		}, recipients, false, slog.New(slog.DiscardHandler))
	}()
	err := waitForRun(t, done)

	// Then
	require.NoError(t, err)
	require.Equal(t, int32(1), client.listens.Load())
}

func newEmptyRecipient(t *testing.T, chatID string, client CallbackClient) Recipient {
	t.Helper()

	return Recipient{
		ChatID:    chatID,
		StatePath: filepath.Join(t.TempDir(), chatID+".db"),
		Callbacks: client,
		Dependencies: app.Dependencies{
			Fetcher:      emptyFetcher{},
			Sender:       noopSender{},
			MessageLimit: 4096,
			Now:          time.Now,
		},
	}
}

func Test_executeJobForChat_and_ForgetLatestForChat_log_chat_id(t *testing.T) {
	t.Parallel()

	// Given
	statePath := filepath.Join(t.TempDir(), "state.db")
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	dependencies := app.Dependencies{
		Fetcher:      emptyFetcher{},
		Sender:       noopSender{},
		MessageLimit: 4096,
		Now:          time.Now,
	}

	// When
	_, err := executeJobForChat(context.Background(), dependencies, statePath, "-1001", logger)
	require.NoError(t, err)
	err = ForgetLatestForChat(context.Background(), statePath, 1, "-1001", logger)

	// Then
	require.NoError(t, err)
	require.Contains(t, logs.String(), `"msg":"digest.started"`)
	require.Contains(t, logs.String(), `"msg":"digest.completed"`)
	require.Contains(t, logs.String(), `"msg":"state.forget_latest.completed"`)
	require.Contains(t, logs.String(), `"chat_id":"-1001"`)
}

type countedErrorFetcher struct {
	calls *atomic.Int32
	err   error
}

func (f countedErrorFetcher) FetchWithResult(context.Context) (alib.FetchResult, error) {
	f.calls.Add(1)

	return alib.FetchResult{}, f.err
}
