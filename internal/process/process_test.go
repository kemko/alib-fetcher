package process

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/app"
	"github.com/kemko/alib-fetcher/internal/store"
	"github.com/kemko/alib-fetcher/internal/telegram"

	"github.com/stretchr/testify/require"
)

func Test_executeJob_returns_result_and_closes_state_database_after_cycle(t *testing.T) {
	t.Parallel()

	// Given
	statePath := filepath.Join(t.TempDir(), "state.db")
	now := time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)
	book := alib.Book{Title: "Книга", BuyURL: "https://example.com/book"}
	dependencies := app.Dependencies{
		Fetcher:      bookFetcher{books: []alib.Book{book}},
		Sender:       noopSender{},
		MessageLimit: 4096,
		Now:          func() time.Time { return now },
	}
	logger := slog.New(slog.DiscardHandler)

	// When
	result, err := executeJob(context.Background(), dependencies, statePath, logger)

	// Then
	require.NoError(t, err)
	require.Equal(t, app.Result{Fetched: 1, New: 1, Sent: 1}, result)
	reopened, err := store.Open(statePath, now)
	require.NoError(t, err)
	require.NoError(t, reopened.Close())
}

func Test_executeJob_returns_partial_result_when_service_fails(t *testing.T) {
	t.Parallel()

	// Given
	statePath := filepath.Join(t.TempDir(), "state.db")
	now := time.Date(2026, time.August, 18, 0, 0, 0, 0, time.UTC)
	book := alib.Book{BuyURL: "https://example.com/old"}
	state, err := store.Open(statePath, now)
	require.NoError(t, err)
	_, err = state.RecordDiscovered(context.Background(), []alib.Book{book}, now)
	require.NoError(t, err)
	require.NoError(t, state.MarkSent(context.Background(), []alib.Book{book}, now.Add(-15*24*time.Hour)))
	require.NoError(t, state.Close())
	fetchErr := errors.New("fetch failed")
	dependencies := app.Dependencies{
		Fetcher:      errorFetcher{err: fetchErr},
		Sender:       noopSender{},
		MessageLimit: 4096,
		Now:          func() time.Time { return now },
	}

	// When
	result, err := executeJob(context.Background(), dependencies, statePath, slog.New(slog.DiscardHandler))

	// Then
	require.ErrorIs(t, err, fetchErr)
	require.Equal(t, app.Result{Pruned: 1}, result)
}

func Test_joinCloseError_preserves_operation_and_close_errors(t *testing.T) {
	t.Parallel()

	// Given
	digestErr := errors.New("digest failed")
	operationErr := digestErr
	closeErr := errors.New("close state failed")

	// When
	joinCloseError(&operationErr, errorCloser{err: closeErr})

	// Then
	require.ErrorIs(t, operationErr, digestErr)
	require.ErrorIs(t, operationErr, closeErr)
}

type errorCloser struct {
	err error
}

func (c errorCloser) Close() error {
	return c.err
}

func Test_ForgetLatest_deletes_records_logs_count_and_closes_state_database(t *testing.T) {
	t.Parallel()

	// Given
	statePath := filepath.Join(t.TempDir(), "state.db")
	now := time.Date(2026, time.August, 18, 0, 0, 0, 0, time.UTC)
	state, err := store.Open(statePath, now)
	require.NoError(t, err)
	books := []alib.Book{
		{BuyURL: "https://example.com/first"},
		{BuyURL: "https://example.com/second"},
		{BuyURL: "https://example.com/third"},
	}
	_, err = state.RecordDiscovered(context.Background(), books, now)
	require.NoError(t, err)
	require.NoError(t, state.MarkSent(context.Background(), []alib.Book{books[2]}, now))
	require.NoError(t, state.Close())

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	// When
	err = ForgetLatest(context.Background(), statePath, 2, logger)

	// Then
	require.NoError(t, err)
	require.Contains(t, logs.String(), `"msg":"state.forget_latest.completed"`)
	require.Contains(t, logs.String(), `"requested":2`)
	require.Contains(t, logs.String(), `"deleted":2`)

	reopened, err := store.Open(statePath, now)
	require.NoError(t, err)
	pending, err := reopened.Pending(context.Background())
	require.NoError(t, err)
	require.NoError(t, reopened.Close())
	require.Equal(t, []alib.Book{books[0]}, pending)
}

func Test_ForgetLatest_logs_actual_count_when_limit_exceeds_database_size(t *testing.T) {
	t.Parallel()

	// Given
	statePath := filepath.Join(t.TempDir(), "state.db")
	now := time.Date(2026, time.August, 18, 0, 0, 0, 0, time.UTC)
	state, err := store.Open(statePath, now)
	require.NoError(t, err)
	books := []alib.Book{
		{BuyURL: "https://example.com/first"},
		{BuyURL: "https://example.com/second"},
	}
	_, err = state.RecordDiscovered(context.Background(), books, now)
	require.NoError(t, err)
	require.NoError(t, state.Close())

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	// When
	err = ForgetLatest(context.Background(), statePath, 5, logger)

	// Then
	require.NoError(t, err)
	require.Contains(t, logs.String(), `"requested":5`)
	require.Contains(t, logs.String(), `"deleted":2`)
	reopened, err := store.Open(statePath, now)
	require.NoError(t, err)
	pending, err := reopened.Pending(context.Background())
	require.NoError(t, err)
	require.NoError(t, reopened.Close())
	require.Empty(t, pending)
}

func Test_ForgetLatest_closes_unchanged_state_without_completion_log_after_cancellation(t *testing.T) {
	t.Parallel()

	// Given
	statePath := filepath.Join(t.TempDir(), "state.db")
	now := time.Date(2026, time.August, 18, 0, 0, 0, 0, time.UTC)
	state, err := store.Open(statePath, now)
	require.NoError(t, err)
	books := []alib.Book{
		{BuyURL: "https://example.com/first"},
		{BuyURL: "https://example.com/second"},
	}
	_, err = state.RecordDiscovered(context.Background(), books, now)
	require.NoError(t, err)
	require.NoError(t, state.Close())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	// When
	err = ForgetLatest(ctx, statePath, 1, logger)

	// Then
	require.ErrorIs(t, err, context.Canceled)
	require.NotContains(t, logs.String(), "state.forget_latest.completed")
	reopened, err := store.Open(statePath, now)
	require.NoError(t, err)
	pending, err := reopened.Pending(context.Background())
	require.NoError(t, err)
	require.NoError(t, reopened.Close())
	require.Equal(t, books, pending)
}

func Test_Run_does_not_listen_for_callbacks_in_once_mode(t *testing.T) {
	t.Parallel()

	// Given
	statePath := filepath.Join(t.TempDir(), "state.db")
	now := time.Date(2026, time.August, 8, 0, 0, 0, 0, time.UTC)
	dependencies := app.Dependencies{
		Fetcher:      emptyFetcher{},
		Sender:       noopSender{},
		MessageLimit: 4096,
		Now:          func() time.Time { return now },
	}
	logger := slog.New(slog.DiscardHandler)

	// When
	err := Run(context.Background(), Settings{
		StatePath: statePath,
	}, dependencies, panicCallbackClient{t: t}, true, logger)

	// Then
	require.NoError(t, err)
}

func Test_Run_service_mode_listens_for_callbacks_runs_startup_and_waits_for_listener(t *testing.T) {
	t.Parallel()

	// Given
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	statePath := filepath.Join(t.TempDir(), "state.db")
	now := time.Date(2026, time.August, 8, 0, 0, 0, 0, time.UTC)
	book := alib.Book{Title: "Книга", BuyURL: "https://example.com/startup"}
	listenerRelease := make(chan struct{})
	callbacks := &blockingRunCallbackClient{
		started:     make(chan struct{}),
		contextDone: make(chan struct{}),
		release:     listenerRelease,
	}
	sender := &cancelingSender{cancel: cancel}
	done := make(chan error, 1)

	// When
	go func() {
		done <- Run(ctx, Settings{
			Location:     time.UTC,
			CronSpec:     "@every 1h",
			StatePath:    statePath,
			RunOnStartup: true,
		}, app.Dependencies{
			Fetcher:      bookFetcher{books: []alib.Book{book}},
			Sender:       sender,
			MessageLimit: 4096,
			Now:          func() time.Time { return now },
		}, callbacks, false, slog.New(slog.DiscardHandler))
	}()
	waitForSignal(t, callbacks.started)
	waitForSignal(t, callbacks.contextDone)
	select {
	case <-done:
		t.Fatal("process stopped before callback listener")
	default:
	}
	close(listenerRelease)
	err := waitForRun(t, done)
	state, openErr := store.Open(statePath, now)
	require.NoError(t, openErr)
	pending, pendingErr := state.Pending(context.Background())
	require.NoError(t, state.Close())

	// Then
	require.NoError(t, err)
	require.NoError(t, pendingErr)
	require.Empty(t, pending)
	require.Equal(t, int32(1), callbacks.listens.Load())
	require.Equal(t, int32(1), sender.sends.Load())
}

func Test_Run_service_mode_skips_startup_when_disabled_but_listens_for_callbacks(t *testing.T) {
	t.Parallel()

	// Given
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var fetches atomic.Int32
	callbacks := &blockingRunCallbackClient{
		started:       make(chan struct{}),
		cancelOnStart: cancel,
	}
	done := make(chan error, 1)

	// When
	go func() {
		done <- Run(ctx, Settings{
			Location:     time.UTC,
			CronSpec:     "@every 1h",
			StatePath:    filepath.Join(t.TempDir(), "state.db"),
			RunOnStartup: false,
		}, app.Dependencies{
			Fetcher:      countingFetcher{calls: &fetches},
			Sender:       noopSender{},
			MessageLimit: 4096,
			Now:          time.Now,
		}, callbacks, false, slog.New(slog.DiscardHandler))
	}()
	waitForSignal(t, callbacks.started)
	err := waitForRun(t, done)

	// Then
	require.NoError(t, err)
	require.Equal(t, int32(1), callbacks.listens.Load())
	require.Zero(t, fetches.Load())
}

func Test_Run_waits_for_refresh_runner_after_listener_stops(t *testing.T) {
	t.Parallel()

	// Given
	ctx, cancel := context.WithCancel(context.Background())
	digestStarted := make(chan struct{})
	releaseDigest := make(chan struct{})
	callbacks := &blockingRunCallbackClient{
		started: make(chan struct{}),
		callbacks: []telegram.Callback{
			{
				ID:            "callback-1",
				Data:          telegram.RefreshCallbackData,
				MessageChatID: -100123,
				MessageID:     77,
			},
		},
	}
	done := make(chan error, 1)

	// When
	go func() {
		done <- Run(ctx, Settings{
			Location:       time.UTC,
			CronSpec:       "@every 1h",
			StatePath:      filepath.Join(t.TempDir(), "state.db"),
			TelegramChatID: "-100123",
			RunOnStartup:   false,
		}, app.Dependencies{
			Fetcher:      uncancelableFetcher{started: digestStarted, release: releaseDigest},
			Sender:       noopSender{},
			MessageLimit: 4096,
			Now:          time.Now,
		}, callbacks, false, slog.New(slog.DiscardHandler))
	}()
	waitForSignal(t, callbacks.started)
	waitForSignal(t, digestStarted)
	cancel()
	select {
	case <-done:
		t.Fatal("process stopped before refresh runner")
	default:
	}
	close(releaseDigest)
	err := waitForRun(t, done)

	// Then
	require.NoError(t, err)
}

type emptyFetcher struct{}

func (emptyFetcher) Fetch(context.Context) ([]alib.Book, error) {
	return nil, nil
}

type bookFetcher struct {
	books []alib.Book
}

func (f bookFetcher) Fetch(context.Context) ([]alib.Book, error) {
	return f.books, nil
}

type noopSender struct{}

func (noopSender) Send(context.Context, string, bool, bool) error {
	return nil
}

type cancelingSender struct {
	cancel context.CancelFunc
	sends  atomic.Int32
}

func (s *cancelingSender) Send(context.Context, string, bool, bool) error {
	s.sends.Add(1)
	s.cancel()

	return nil
}

type panicCallbackClient struct {
	t *testing.T
}

func (c panicCallbackClient) ListenCallbacks(
	context.Context,
	telegram.CallbackHandler,
	telegram.CallbackErrorHandler,
) {
	c.t.Fatal("callback listener should not start")
}

func (c panicCallbackClient) AnswerCallback(context.Context, string, string) error {
	c.t.Fatal("callback answering should not run")

	return nil
}

func (c panicCallbackClient) RemoveReplyMarkup(context.Context, int64, int) error {
	c.t.Fatal("reply markup removal should not run")

	return nil
}

type blockingRunCallbackClient struct {
	started       chan struct{}
	contextDone   chan struct{}
	release       <-chan struct{}
	cancelOnStart context.CancelFunc
	callbacks     []telegram.Callback
	listens       atomic.Int32
}

func (c *blockingRunCallbackClient) ListenCallbacks(
	ctx context.Context,
	handle telegram.CallbackHandler,
	_ telegram.CallbackErrorHandler,
) {
	if c.listens.Add(1) == 1 {
		close(c.started)
	}
	for _, callback := range c.callbacks {
		handle(ctx, callback)
	}
	if c.cancelOnStart != nil {
		c.cancelOnStart()
	}
	<-ctx.Done()
	if c.contextDone != nil {
		close(c.contextDone)
	}
	if c.release != nil {
		<-c.release
	}
}

func (c *blockingRunCallbackClient) AnswerCallback(context.Context, string, string) error {
	return nil
}

func (c *blockingRunCallbackClient) RemoveReplyMarkup(context.Context, int64, int) error {
	return nil
}

type uncancelableFetcher struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (f uncancelableFetcher) Fetch(context.Context) ([]alib.Book, error) {
	close(f.started)
	<-f.release

	return nil, nil
}

func waitForSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()

	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for signal")
	}
}

func waitForRun(t *testing.T, done <-chan error) error {
	t.Helper()

	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("process did not stop")

		return nil
	}
}
