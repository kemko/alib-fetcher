package main

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

	"github.com/robfig/cron/v3"
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

func Test_runScheduler_executes_job_immediately_before_waiting_for_schedule(t *testing.T) {
	// Given
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	scheduler := cron.New()
	var runs atomic.Int32
	job := func() {
		runs.Add(1)
	}
	_, err := scheduler.AddFunc("0 0 1 1 *", job)
	require.NoError(t, err)

	// When
	runScheduler(ctx, scheduler, job, true)

	// Then
	require.Equal(t, int32(1), runs.Load())
}

func Test_runScheduler_skips_job_at_startup_when_disabled(t *testing.T) {
	// Given
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	scheduler := cron.New()
	var runs atomic.Int32
	job := func() {
		runs.Add(1)
	}
	_, err := scheduler.AddFunc("0 0 1 1 *", job)
	require.NoError(t, err)

	// When
	runScheduler(ctx, scheduler, job, false)

	// Then
	require.Zero(t, runs.Load())
}

type emptyFetcher struct{}

func (emptyFetcher) Fetch(context.Context) ([]alib.Book, error) {
	return nil, nil
}

type noopSender struct{}

func (noopSender) Send(context.Context, string, bool) error {
	return nil
}
