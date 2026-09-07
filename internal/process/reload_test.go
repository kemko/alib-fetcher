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

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/app"
	"github.com/kemko/alib-fetcher/internal/store"
	"github.com/kemko/alib-fetcher/internal/telegram"
)

func TestRunReloadable_answers_callbacks_while_draining_old_delivery(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs synchronizedBuffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	deliveryStarted := make(chan struct{})
	releaseDelivery := make(chan struct{})
	updates := make(chan telegram.Callback, 1)
	client := &recordingCallbackClient{updates: updates}
	book := alib.Book{Title: "Old snapshot", BuyURL: "https://example.com/old"}
	initial := reloadTestSnapshot(t, "-1001", bookFetcher{books: []alib.Book{book}}, true)
	initial.Recipients[0].Callbacks = client
	initial.Recipients[0].Dependencies.Sender = &recordingSender{afterSend: func() {
		close(deliveryStarted)
		select {
		case <-releaseDelivery:
		case <-ctx.Done():
		}
	}}
	var candidateFetches atomic.Int32
	candidate := reloadTestSnapshot(t, "-1001", countingFetcher{calls: &candidateFetches}, true)
	candidate.Key = "candidate"
	candidate.Recipients[0].StatePath = initial.Recipients[0].StatePath
	candidate.Recipients[0].Callbacks = client
	reloader := &reloadSequence{calls: new(atomic.Int32), candidate: candidate}
	done := make(chan error, 1)

	go func() { done <- RunReloadable(ctx, initial, reloader.Load, logger) }()
	waitForSignal(t, deliveryStarted)
	waitForLog(t, &logs, "config.reload_pending")
	updates <- telegram.Callback{ID: "during-drain", Data: telegram.RefreshCallbackData, MessageChatID: -1001}
	require.Eventually(t, func() bool { return len(client.answersSnapshot()) == 1 }, time.Second, time.Millisecond)
	require.Equal(t, []callbackAnswer{{id: "during-drain", text: refreshAlreadyRunningText}}, client.answersSnapshot())
	require.Equal(t, int32(1), client.listens.Load())
	require.Zero(t, client.stops.Load())
	require.Equal(t, int32(1), reloader.calls.Load())
	require.Zero(t, candidateFetches.Load())

	close(releaseDelivery)
	waitForLog(t, &logs, "config.reloaded")
	waitForAtomicAtLeast(t, &client.listens, 2)
	cancel()
	require.NoError(t, waitForRun(t, done))
	require.Zero(t, candidateFetches.Load())
	require.Equal(t, int32(2), client.stops.Load())
	state, err := store.Open(initial.Recipients[0].StatePath, time.Now())
	require.NoError(t, err)
	pending, err := state.Pending(context.Background())
	require.NoError(t, err)
	require.NoError(t, state.Close())
	require.Empty(t, pending)
	require.Less(t, bytes.Index(logs.Bytes(), []byte("digest.completed")), bytes.Index(logs.Bytes(), []byte("config.reloaded")))
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs synchronizedBuffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	updates := make(chan telegram.Callback, 1)
	client := &recordingCallbackClient{updates: updates}
	book := alib.Book{Title: "Original", BuyURL: "https://example.com/original"}
	initial := reloadTestSnapshot(t, "-1001", bookFetcher{books: []alib.Book{book}}, false)
	initial.Recipients[0].Callbacks = client
	sender := &recordingSender{}
	initial.Recipients[0].Dependencies.Sender = sender
	var candidateFetches atomic.Int32
	candidate := reloadTestSnapshot(t, "candidate", countingFetcher{calls: &candidateFetches}, false)
	reloader := &reloadSequence{calls: new(atomic.Int32), candidate: candidate, failAfterFirst: true}
	done := make(chan error, 1)

	go func() { done <- RunReloadable(ctx, initial, reloader.Load, logger) }()
	waitForLog(t, &logs, "config.reload_failed")
	waitForAtomicAtLeast(t, &client.listens, 2)
	updates <- telegram.Callback{ID: "after-rollback", Data: telegram.RefreshCallbackData, MessageChatID: -1001}
	waitForLog(t, &logs, "digest.completed")
	cancel()

	require.NoError(t, waitForRun(t, done))
	require.NotContains(t, logs.String(), `"msg":"config.reloaded"`)
	require.Equal(t, []callbackAnswer{{id: "after-rollback", text: refreshStartedText}}, client.answersSnapshot())
	require.Len(t, sender.messages, 1)
	require.Contains(t, sender.messages[0], book.Title)
	require.Zero(t, candidateFetches.Load())
	require.NoFileExists(t, candidate.Recipients[0].StatePath)
	state, err := store.Open(initial.Recipients[0].StatePath, time.Now())
	require.NoError(t, err)
	pending, err := state.Pending(context.Background())
	require.NoError(t, err)
	require.NoError(t, state.Close())
	require.Empty(t, pending)
}

func Test_reloadGeneration_suppresses_only_consecutive_identical_errors(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs synchronizedBuffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	initial := reloadTestSnapshot(t, "initial", emptyFetcher{}, false)
	generation, err := startServiceGeneration(ctx, initial, nil, logger)
	require.NoError(t, err)
	defer generation.stop()
	lastError := ""
	for index, message := range []string{"invalid candidate", "invalid candidate", "different candidate"} {
		load := func(context.Context) (ReloadSnapshot, error) { return ReloadSnapshot{}, errors.New(message) }
		next, latestError, reloadErr := reloadGeneration(ctx, generation, load, lastError, logger)
		require.NoError(t, reloadErr)
		require.Same(t, generation, next)
		lastError = latestError
		require.Equal(t, 1+index/2, bytes.Count(logs.Bytes(), []byte(`"msg":"config.reload_failed"`)))
	}
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
	candidate      ReloadSnapshot
	failAfterFirst bool
}

func (sequence *reloadSequence) Load(context.Context) (ReloadSnapshot, error) {
	call := sequence.calls.Add(1)
	if sequence.failAfterFirst && call > 1 {
		return ReloadSnapshot{}, errors.New("invalid candidate")
	}

	return sequence.candidate, nil
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
