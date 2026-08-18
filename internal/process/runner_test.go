package process

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/app"
	"github.com/kemko/alib-fetcher/internal/store"

	"github.com/stretchr/testify/require"
)

func Test_digestRunner_skips_scheduled_digest_when_another_digest_is_running(t *testing.T) {
	t.Parallel()

	// Given
	var fetches atomic.Int32
	runner := newDigestRunner(app.Dependencies{
		Fetcher:      countingFetcher{calls: &fetches},
		Sender:       noopSender{},
		MessageLimit: 4096,
		Now:          time.Now,
	}, filepath.Join(t.TempDir(), "state.db"), slog.New(slog.DiscardHandler))
	runner.lock.Lock()
	defer runner.lock.Unlock()

	// When
	runner.runScheduled(context.Background())

	// Then
	require.Zero(t, fetches.Load())
}

func Test_digestRunner_shares_lock_across_startup_scheduled_and_refresh_digests(t *testing.T) {
	t.Parallel()

	// Given
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	digestStarted := make(chan struct{})
	releaseDigest := make(chan struct{})
	var fetches atomic.Int32
	runner := newDigestRunner(app.Dependencies{
		Fetcher: &blockingFetcher{
			calls:   &fetches,
			started: digestStarted,
			release: releaseDigest,
		},
		Sender:       noopSender{},
		MessageLimit: 4096,
		Now:          time.Now,
	}, filepath.Join(t.TempDir(), "state.db"), slog.New(slog.DiscardHandler))
	startupDone := make(chan struct{})

	// When
	go func() {
		runner.runStartup(ctx)
		close(startupDone)
	}()
	waitForSignal(t, digestStarted)
	runner.runScheduled(ctx)
	refreshStarted := runner.tryStartRefresh(ctx, nil, nil)
	close(releaseDigest)
	waitForSignal(t, startupDone)
	runner.wait()

	// Then
	require.False(t, refreshStarted)
	require.Equal(t, int32(1), fetches.Load())
}

func Test_digestRunner_logs_trigger_when_digest_fails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		run     func(*testing.T, context.Context, *digestRunner)
		name    string
		trigger string
	}{
		{
			name:    "startup",
			trigger: triggerStartup,
			run: func(_ *testing.T, ctx context.Context, runner *digestRunner) {
				runner.runStartup(ctx)
			},
		},
		{
			name:    "scheduled",
			trigger: triggerScheduled,
			run: func(_ *testing.T, ctx context.Context, runner *digestRunner) {
				runner.runScheduled(ctx)
			},
		},
		{
			name:    "refresh",
			trigger: triggerRefresh,
			run: func(t *testing.T, ctx context.Context, runner *digestRunner) {
				t.Helper()

				require.True(t, runner.tryStartRefresh(ctx, nil, nil))
				runner.wait()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Given
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			runner := newDigestRunner(app.Dependencies{
				Fetcher:      errorFetcher{err: errors.New("fetch failed")},
				Sender:       noopSender{},
				MessageLimit: 4096,
				Now:          time.Now,
			}, filepath.Join(t.TempDir(), "state.db"), logger)

			// When
			tt.run(t, context.Background(), runner)

			// Then
			require.Contains(t, logs.String(), "msg=digest.failed")
			require.Contains(t, logs.String(), "trigger="+tt.trigger)
		})
	}
}

func Test_digestRunner_passes_refresh_result_and_error_to_completion_hook(t *testing.T) {
	t.Parallel()

	// Given
	ctx := context.Background()
	book := alib.Book{Title: "Книга", BuyURL: "https://example.com/book"}
	resultCh := make(chan app.Result, 1)
	errCh := make(chan error, 1)
	runner := newDigestRunner(app.Dependencies{
		Fetcher:      bookFetcher{books: []alib.Book{book}},
		Sender:       noopSender{},
		MessageLimit: 4096,
		Now:          time.Now,
	}, filepath.Join(t.TempDir(), "state.db"), slog.New(slog.DiscardHandler))

	// When
	require.True(t, runner.tryStartRefresh(ctx, nil, func(result app.Result, err error) {
		resultCh <- result
		errCh <- err
	}))
	runner.wait()

	// Then
	require.Equal(t, app.Result{Fetched: 1, New: 1, Sent: 1}, <-resultCh)
	require.NoError(t, <-errCh)
}

func Test_digestRunner_passes_partial_result_and_error_to_completion_hook(t *testing.T) {
	t.Parallel()

	// Given
	ctx := context.Background()
	now := time.Date(2026, time.August, 18, 0, 0, 0, 0, time.UTC)
	statePath := filepath.Join(t.TempDir(), "state.db")
	book := alib.Book{BuyURL: "https://example.com/old"}
	state, err := store.Open(statePath, now)
	require.NoError(t, err)
	_, err = state.RecordDiscovered(ctx, []alib.Book{book}, now)
	require.NoError(t, err)
	require.NoError(t, state.MarkSent(ctx, []alib.Book{book}, now.Add(-15*24*time.Hour)))
	require.NoError(t, state.Close())
	fetchErr := errors.New("fetch failed")
	resultCh := make(chan app.Result, 1)
	errCh := make(chan error, 1)
	runner := newDigestRunner(app.Dependencies{
		Fetcher:      errorFetcher{err: fetchErr},
		Sender:       noopSender{},
		MessageLimit: 4096,
		Now:          func() time.Time { return now },
	}, statePath, slog.New(slog.DiscardHandler))

	// When
	require.True(t, runner.tryStartRefresh(ctx, nil, func(result app.Result, err error) {
		resultCh <- result
		errCh <- err
	}))
	runner.wait()

	// Then
	require.Equal(t, app.Result{Pruned: 1}, <-resultCh)
	require.ErrorIs(t, <-errCh, fetchErr)
}

type countingFetcher struct {
	calls *atomic.Int32
}

func (f countingFetcher) Fetch(context.Context) ([]alib.Book, error) {
	f.calls.Add(1)

	return nil, nil
}

type blockingFetcher struct {
	calls   *atomic.Int32
	started chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (f *blockingFetcher) Fetch(ctx context.Context) ([]alib.Book, error) {
	if f.calls != nil {
		f.calls.Add(1)
	}
	f.once.Do(func() {
		close(f.started)
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.release:
		return nil, nil
	}
}

type errorFetcher struct {
	err error
}

func (f errorFetcher) Fetch(context.Context) ([]alib.Book, error) {
	return nil, f.err
}
