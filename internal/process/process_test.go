package process

import (
	"context"
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

func Test_executeJob_closes_state_database_after_cycle(t *testing.T) {
	t.Parallel()

	// Given
	statePath := filepath.Join(t.TempDir(), "state.db")
	now := time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)
	dependencies := app.Dependencies{
		Fetcher:      emptyFetcher{},
		Sender:       noopSender{},
		MessageLimit: 4096,
		Now:          func() time.Time { return now },
	}
	logger := slog.New(slog.DiscardHandler)

	// When
	err := executeJob(context.Background(), dependencies, statePath, logger)

	// Then
	require.NoError(t, err)
	reopened, err := store.Open(statePath, now)
	require.NoError(t, err)
	require.NoError(t, reopened.Close())
}

func Test_Run_does_not_poll_callbacks_in_once_mode(t *testing.T) {
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

func Test_Run_service_mode_polls_callbacks_runs_startup_and_stops_cleanly(t *testing.T) {
	t.Parallel()

	// Given
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	statePath := filepath.Join(t.TempDir(), "state.db")
	now := time.Date(2026, time.August, 8, 0, 0, 0, 0, time.UTC)
	book := alib.Book{Title: "Книга", BuyURL: "https://example.com/startup"}
	callbacks := &blockingRunCallbackClient{firstPoll: make(chan struct{})}
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
	waitForSignal(t, callbacks.firstPoll)
	err := waitForRun(t, done)
	state, openErr := store.Open(statePath, now)
	require.NoError(t, openErr)
	pending, pendingErr := state.Pending(context.Background())
	require.NoError(t, state.Close())

	// Then
	require.NoError(t, err)
	require.NoError(t, pendingErr)
	require.Empty(t, pending)
	require.Equal(t, int32(1), callbacks.polls.Load())
	require.Equal(t, int32(1), sender.sends.Load())
}

func Test_Run_service_mode_skips_startup_when_disabled_but_polls_callbacks(t *testing.T) {
	t.Parallel()

	// Given
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var fetches atomic.Int32
	callbacks := &blockingRunCallbackClient{
		firstPoll:         make(chan struct{}),
		cancelOnFirstPoll: cancel,
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
	waitForSignal(t, callbacks.firstPoll)
	err := waitForRun(t, done)

	// Then
	require.NoError(t, err)
	require.Equal(t, int32(1), callbacks.polls.Load())
	require.Zero(t, fetches.Load())
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

func (c panicCallbackClient) PollCallbacks(context.Context, int) ([]telegram.Callback, int, error) {
	c.t.Fatal("callback polling should not start")

	return nil, 0, nil
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
	firstPoll         chan struct{}
	cancelOnFirstPoll context.CancelFunc
	polls             atomic.Int32
}

func (c *blockingRunCallbackClient) PollCallbacks(ctx context.Context, offset int) ([]telegram.Callback, int, error) {
	if c.polls.Add(1) == 1 {
		close(c.firstPoll)
		if c.cancelOnFirstPoll != nil {
			c.cancelOnFirstPoll()
		}
	}
	<-ctx.Done()

	return nil, offset, ctx.Err()
}

func (c *blockingRunCallbackClient) AnswerCallback(context.Context, string, string) error {
	return nil
}

func (c *blockingRunCallbackClient) RemoveReplyMarkup(context.Context, int64, int) error {
	return nil
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
