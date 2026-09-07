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

	"github.com/stretchr/testify/require"

	"github.com/kemko/alib-fetcher/internal/app"
)

func TestRunReloadable_waits_for_inflight_digest_before_restarting_polling(t *testing.T) {
	t.Parallel()

	// Given
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs synchronizedBuffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	digestStarted := make(chan struct{})
	releaseDigest := make(chan struct{})
	var fetches atomic.Int32
	initial := reloadTestSnapshot(t, "initial", &blockingFetcher{
		calls:   &fetches,
		started: digestStarted,
		release: releaseDigest,
	}, true)
	var candidateFetches atomic.Int32
	candidate := reloadTestSnapshot(t, "candidate", countingFetcher{calls: &candidateFetches}, true)
	candidate.Recipients[0].ChatID = initial.Recipients[0].ChatID
	candidate.Recipients[0].StatePath = initial.Recipients[0].StatePath
	var reloadCalls atomic.Int32
	firstReload := make(chan struct{})
	reloader := &reloadSequence{
		calls:     &reloadCalls,
		first:     firstReload,
		candidate: candidate,
	}
	done := make(chan error, 1)

	// When
	go func() {
		done <- RunReloadable(ctx, initial, reloader.Load, logger)
	}()
	waitForSignal(t, digestStarted)
	waitForSignal(t, firstReload)
	close(releaseDigest)
	waitForReloadCall(t, &reloadCalls, 2)
	waitForLog(t, &logs, "config.reloaded")
	cancel()

	// Then
	require.NoError(t, waitForRun(t, done))
	require.Equal(t, int32(1), fetches.Load())
	require.Zero(t, candidateFetches.Load())
	require.Contains(t, logs.String(), `"msg":"config.reload_pending"`)
	require.Contains(t, logs.String(), `"msg":"config.reloaded"`)
}

func TestRunReloadable_reuses_one_shared_poller_and_applies_new_schedule(t *testing.T) {
	t.Parallel()

	// Given
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs synchronizedBuffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	var initialFetches atomic.Int32
	var candidateFetches atomic.Int32
	client := &recordingCallbackClient{}
	initial := reloadTestSnapshot(t, "first", countingFetcher{calls: &initialFetches}, false)
	initial.Settings.CronSpec = "@every 1h"
	initial.Recipients[0].Callbacks = client
	secondInitial := initial.Recipients[0]
	secondInitial.ChatID = "second"
	secondInitial.StatePath = filepath.Join(t.TempDir(), "second.db")
	initial.Recipients = append(initial.Recipients, secondInitial)
	initial.Key = "initial"

	candidate := reloadTestSnapshot(t, "first", countingFetcher{calls: &candidateFetches}, false)
	candidate.Settings.CronSpec = "@every 1s"
	candidate.Recipients[0].Callbacks = client
	secondCandidate := candidate.Recipients[0]
	secondCandidate.ChatID = "second"
	secondCandidate.StatePath = filepath.Join(t.TempDir(), "second-candidate.db")
	candidate.Recipients = append(candidate.Recipients, secondCandidate)
	candidate.Key = "candidate"
	reloader := &reloadSequence{calls: new(atomic.Int32), candidate: candidate}
	done := make(chan error, 1)

	// When
	go func() {
		done <- RunReloadable(ctx, initial, reloader.Load, logger)
	}()
	waitForLog(t, &logs, "config.reloaded")
	waitForAtomicAtLeast(t, &candidateFetches, 2)
	cancel()

	// Then
	require.NoError(t, waitForRun(t, done))
	require.Zero(t, initialFetches.Load())
	require.GreaterOrEqual(t, candidateFetches.Load(), int32(2))
	require.Equal(t, int32(2), client.listens.Load())
}

func TestRunReloadable_restores_previous_generation_when_recheck_fails(t *testing.T) {
	t.Parallel()

	// Given
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs synchronizedBuffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	initial := reloadTestSnapshot(t, "initial", emptyFetcher{}, false)
	candidate := reloadTestSnapshot(t, "candidate", emptyFetcher{}, false)
	var reloadCalls atomic.Int32
	reloader := &reloadSequence{
		calls:          &reloadCalls,
		candidate:      candidate,
		failAfterFirst: true,
	}
	done := make(chan error, 1)

	// When
	go func() {
		done <- RunReloadable(ctx, initial, reloader.Load, logger)
	}()
	waitForReloadCall(t, &reloadCalls, 2)
	waitForLog(t, &logs, "config.reload_failed")
	cancel()

	// Then
	require.NoError(t, waitForRun(t, done))
	require.NotContains(t, logs.String(), `"msg":"config.reloaded"`)
	require.Equal(t, 1, bytes.Count(logs.Bytes(), []byte(`"msg":"config.reload_failed"`)))
}

func TestRunReloadable_does_not_repeat_the_same_reload_error(t *testing.T) {
	t.Parallel()

	// Given
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs synchronizedBuffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	initial := reloadTestSnapshot(t, "initial", emptyFetcher{}, false)
	reloader := &reloadSequence{
		calls:          new(atomic.Int32),
		candidate:      initial,
		failAfterFirst: true,
	}
	done := make(chan error, 1)

	// When
	go func() {
		done <- RunReloadable(ctx, initial, reloader.Load, logger)
	}()
	waitForReloadCall(t, reloader.calls, 2)
	cancel()

	// Then
	require.NoError(t, waitForRun(t, done))
	require.Equal(t, 1, bytes.Count(logs.Bytes(), []byte(`"msg":"config.reload_failed"`)))
}

func Test_digestRunner_rejects_new_work_after_stop_requested(t *testing.T) {
	t.Parallel()

	// Given
	var fetches atomic.Int32
	runner := newDigestRunner(app.Dependencies{
		Fetcher:      countingFetcher{calls: &fetches},
		Sender:       noopSender{},
		MessageLimit: 4096,
		Now:          time.Now,
	}, filepath.Join(t.TempDir(), "state.db"), slog.New(slog.DiscardHandler))
	runner.requestStop()

	// When
	runner.runScheduled(context.Background())

	// Then
	require.Zero(t, fetches.Load())
}

func reloadTestSnapshot(t *testing.T, key string, fetcher app.Fetcher, runOnStartup bool) ReloadSnapshot {
	t.Helper()

	return ReloadSnapshot{
		Settings: Settings{
			CronSpec:     "@every 1h",
			Location:     time.UTC,
			RunOnStartup: runOnStartup,
		},
		Recipients: []Recipient{{
			ChatID:    key,
			StatePath: filepath.Join(t.TempDir(), key+".db"),
			Dependencies: app.Dependencies{
				Fetcher:      fetcher,
				Sender:       noopSender{},
				MessageLimit: 4096,
				Now:          time.Now,
			},
		}},
		Key: key,
	}
}

type reloadSequence struct {
	calls          *atomic.Int32
	first          chan<- struct{}
	candidate      ReloadSnapshot
	failAfterFirst bool
}

func (sequence *reloadSequence) Load(context.Context) (ReloadSnapshot, error) {
	call := sequence.calls.Add(1)
	if call == 1 && sequence.first != nil {
		close(sequence.first)
	}
	if sequence.failAfterFirst && call > 1 {
		return ReloadSnapshot{}, errors.New("invalid candidate")
	}

	return sequence.candidate, nil
}

func waitForReloadCall(t *testing.T, calls *atomic.Int32, expected int32) {
	t.Helper()
	select {
	case <-waitForReloadCallSignal(calls, expected):
	case <-time.After(3 * time.Second):
		t.Fatalf("reload call count did not reach %d", expected)
	}
}

func waitForReloadCallSignal(calls *atomic.Int32, expected int32) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		for calls.Load() < expected {
			time.Sleep(10 * time.Millisecond)
		}
		close(done)
	}()

	return done
}

func waitForAtomicAtLeast(t *testing.T, value *atomic.Int32, expected int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if value.Load() >= expected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("value did not reach %d", expected)
}

func waitForLog(t *testing.T, logs *synchronizedBuffer, message string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Contains(logs.Bytes(), []byte(`"msg":"`+message+`"`)) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("log %q was not emitted", message)
}

type synchronizedBuffer struct {
	bytes.Buffer
	mu sync.Mutex
}

func (buffer *synchronizedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()

	return buffer.Buffer.Write(value)
}

func (buffer *synchronizedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()

	return buffer.Buffer.String()
}

func (buffer *synchronizedBuffer) Bytes() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()

	return append([]byte(nil), buffer.Buffer.Bytes()...)
}
