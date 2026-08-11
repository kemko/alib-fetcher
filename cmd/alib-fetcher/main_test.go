package main

import (
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
	"github.com/kemko/alib-fetcher/internal/telegram"

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

func Test_runProcess_does_not_poll_callbacks_in_once_mode(t *testing.T) {
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
	err := runProcess(context.Background(), processSettings{
		StatePath: statePath,
	}, dependencies, panicCallbackClient{t: t}, true, logger)

	// Then
	require.NoError(t, err)
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

func Test_digestRunner_skips_scheduled_digest_when_another_digest_is_running(t *testing.T) {
	t.Parallel()

	// Given
	statePath := filepath.Join(t.TempDir(), "state.db")
	var fetches atomic.Int32
	runner := &digestRunner{dependencies: app.Dependencies{
		Fetcher:      countingFetcher{calls: &fetches},
		Sender:       noopSender{},
		MessageLimit: 4096,
		Now:          time.Now,
	}, statePath: statePath, logger: slog.New(slog.DiscardHandler)}
	runner.lock.Lock()
	defer runner.lock.Unlock()

	// When
	runner.RunScheduled(context.Background())

	// Then
	require.Zero(t, fetches.Load())
}

func Test_pollRefreshCallbacks_advances_offset_and_ignores_unknown_callback_data(t *testing.T) {
	t.Parallel()

	// Given
	ctx, cancel := context.WithCancel(context.Background())
	client := &recordingCallbackClient{
		callbacks: []telegram.Callback{
			{
				ID:            "callback-1",
				Data:          "unknown",
				MessageChatID: -100123,
				MessageID:     77,
			},
		},
		nextOffset: 101,
		cancel:     cancel,
	}
	runner := &digestRunner{dependencies: app.Dependencies{
		Fetcher:      emptyFetcher{},
		Sender:       noopSender{},
		MessageLimit: 4096,
		Now:          time.Now,
	}, statePath: filepath.Join(t.TempDir(), "state.db"), logger: slog.New(slog.DiscardHandler)}

	// When
	done := startCallbackPolling(ctx, client, runner, slog.New(slog.DiscardHandler))
	waitForCallbackLoop(t, done)

	// Then
	require.Equal(t, []int{0, 101}, client.pollOffsetsSnapshot())
	require.Empty(t, client.answersSnapshot())
	require.Empty(t, client.removalsSnapshot())
}

func Test_pollRefreshCallbacks_runs_digest_and_removes_old_button_before_new_send(t *testing.T) {
	t.Parallel()

	// Given
	ctx, cancel := context.WithCancel(context.Background())
	statePath := filepath.Join(t.TempDir(), "state.db")
	now := time.Date(2026, time.August, 8, 0, 0, 0, 0, time.UTC)
	book := alib.Book{Title: "Новая книга", BuyURL: "https://example.com/book"}
	events := &recordedEvents{}
	client := &recordingCallbackClient{
		callbacks: []telegram.Callback{
			{
				ID:            "callback-1",
				Data:          telegram.RefreshCallbackData,
				MessageChatID: -100123,
				MessageID:     77,
			},
		},
		nextOffset: 101,
		events:     events,
	}
	sender := &recordingSender{events: events, afterSend: cancel}
	runner := &digestRunner{dependencies: app.Dependencies{
		Fetcher:      bookFetcher{books: []alib.Book{book}},
		Sender:       sender,
		MessageLimit: 4096,
		Now:          func() time.Time { return now },
	}, statePath: statePath, logger: slog.New(slog.DiscardHandler)}

	// When
	done := startCallbackPolling(ctx, client, runner, slog.New(slog.DiscardHandler))
	waitForCallbackLoop(t, done)
	runner.Wait()
	state, err := store.Open(statePath, now)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, state.Close()) })
	pending, err := state.Pending(context.Background())

	// Then
	require.NoError(t, err)
	require.Empty(t, pending)
	require.Len(t, sender.messages, 1)
	require.Equal(t, []callbackAnswer{{id: "callback-1", text: refreshStartedText}}, client.answersSnapshot())
	require.Equal(t, []removedReplyMarkup{{chatID: -100123, messageID: 77}}, client.removalsSnapshot())
	require.Equal(t, []string{"answer", "remove", "send"}, events.snapshot())
}

func Test_handleRefreshCallback_leaves_old_button_when_digest_sends_no_books(t *testing.T) {
	t.Parallel()

	// Given
	statePath := filepath.Join(t.TempDir(), "state.db")
	now := time.Date(2026, time.August, 8, 0, 0, 0, 0, time.UTC)
	client := &recordingCallbackClient{}
	sender := &recordingSender{}
	runner := &digestRunner{dependencies: app.Dependencies{
		Fetcher:      emptyFetcher{},
		Sender:       sender,
		MessageLimit: 4096,
		Now:          func() time.Time { return now },
	}, statePath: statePath, logger: slog.New(slog.DiscardHandler)}

	// When
	handleRefreshCallback(context.Background(), client, runner, telegram.Callback{
		ID:            "callback-1",
		Data:          telegram.RefreshCallbackData,
		MessageChatID: -100123,
		MessageID:     77,
	}, slog.New(slog.DiscardHandler))
	runner.Wait()

	// Then
	require.Equal(t, []callbackAnswer{{id: "callback-1", text: refreshStartedText}}, client.answersSnapshot())
	require.Empty(t, client.removalsSnapshot())
	require.Empty(t, sender.messages)
}

func Test_handleRefreshCallback_answers_and_skips_when_digest_is_running(t *testing.T) {
	t.Parallel()

	// Given
	statePath := filepath.Join(t.TempDir(), "state.db")
	client := &recordingCallbackClient{}
	sender := &recordingSender{}
	runner := &digestRunner{dependencies: app.Dependencies{
		Fetcher:      emptyFetcher{},
		Sender:       sender,
		MessageLimit: 4096,
		Now:          time.Now,
	}, statePath: statePath, logger: slog.New(slog.DiscardHandler)}
	runner.lock.Lock()
	defer runner.lock.Unlock()

	// When
	handleRefreshCallback(context.Background(), client, runner, telegram.Callback{
		ID:            "callback-1",
		Data:          telegram.RefreshCallbackData,
		MessageChatID: -100123,
		MessageID:     77,
	}, slog.New(slog.DiscardHandler))

	// Then
	require.Equal(t, []callbackAnswer{{id: "callback-1", text: refreshAlreadyRunningText}}, client.answersSnapshot())
	require.Empty(t, client.removalsSnapshot())
	require.Empty(t, sender.messages)
}

func Test_handleRefreshCallback_answers_duplicate_refresh_while_background_digest_runs(t *testing.T) {
	t.Parallel()

	// Given
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	statePath := filepath.Join(t.TempDir(), "state.db")
	digestStarted := make(chan struct{})
	releaseDigest := make(chan struct{})
	client := &recordingCallbackClient{}
	runner := &digestRunner{
		dependencies: app.Dependencies{
			Fetcher:      &blockingFetcher{started: digestStarted, release: releaseDigest},
			Sender:       noopSender{},
			MessageLimit: 4096,
			Now:          time.Now,
		},
		statePath: statePath,
		logger:    slog.New(slog.DiscardHandler),
	}

	// When
	handleRefreshCallback(ctx, client, runner, telegram.Callback{
		ID:            "callback-1",
		Data:          telegram.RefreshCallbackData,
		MessageChatID: -100123,
		MessageID:     77,
	}, slog.New(slog.DiscardHandler))
	waitForSignal(t, digestStarted)
	handleRefreshCallback(ctx, client, runner, telegram.Callback{
		ID:            "callback-2",
		Data:          telegram.RefreshCallbackData,
		MessageChatID: -100123,
		MessageID:     77,
	}, slog.New(slog.DiscardHandler))
	close(releaseDigest)
	runner.Wait()

	// Then
	require.Equal(t, []callbackAnswer{
		{id: "callback-1", text: refreshStartedText},
		{id: "callback-2", text: refreshAlreadyRunningText},
	}, client.answersSnapshot())
}

func Test_pollRefreshCallbacks_backs_off_after_poll_error(t *testing.T) {
	t.Parallel()

	// Given
	ctx, cancel := context.WithCancel(context.Background())
	client := &failingCallbackClient{
		firstPoll: make(chan struct{}),
		err:       errors.New("telegram unavailable"),
	}
	runner := &digestRunner{
		dependencies: app.Dependencies{
			Fetcher:      emptyFetcher{},
			Sender:       noopSender{},
			MessageLimit: 4096,
			Now:          time.Now,
		},
		statePath: filepath.Join(t.TempDir(), "state.db"),
		logger:    slog.New(slog.DiscardHandler),
	}

	// When
	done := startCallbackPolling(ctx, client, runner, slog.New(slog.DiscardHandler))
	waitForSignal(t, client.firstPoll)
	time.Sleep(20 * time.Millisecond)
	cancel()
	waitForCallbackLoop(t, done)

	// Then
	require.Equal(t, int32(1), client.polls.Load())
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

type countingFetcher struct {
	calls *atomic.Int32
}

func (f countingFetcher) Fetch(context.Context) ([]alib.Book, error) {
	f.calls.Add(1)

	return nil, nil
}

type blockingFetcher struct {
	started chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (f *blockingFetcher) Fetch(ctx context.Context) ([]alib.Book, error) {
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

type noopSender struct{}

func (noopSender) Send(context.Context, string, bool, bool) error {
	return nil
}

type recordingSender struct {
	events        *recordedEvents
	afterSend     func()
	messages      []string
	silent        []bool
	attachRefresh []bool
}

func (s *recordingSender) Send(_ context.Context, text string, silent bool, attachRefresh bool) error {
	s.messages = append(s.messages, text)
	s.silent = append(s.silent, silent)
	s.attachRefresh = append(s.attachRefresh, attachRefresh)
	if s.events != nil {
		s.events.append("send")
	}
	if s.afterSend != nil {
		s.afterSend()
	}

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

type recordingCallbackClient struct {
	events      *recordedEvents
	cancel      context.CancelFunc
	callbacks   []telegram.Callback
	answers     []callbackAnswer
	removals    []removedReplyMarkup
	pollOffsets []int
	nextOffset  int
	polls       int
	mu          sync.Mutex
}

func (c *recordingCallbackClient) PollCallbacks(ctx context.Context, offset int) ([]telegram.Callback, int, error) {
	c.mu.Lock()
	c.pollOffsets = append(c.pollOffsets, offset)
	if c.polls == 0 {
		c.polls++
		callbacks := c.callbacks
		nextOffset := c.nextOffset
		c.mu.Unlock()

		return callbacks, nextOffset, nil
	}
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	<-ctx.Done()

	return nil, offset, ctx.Err()
}

func (c *recordingCallbackClient) AnswerCallback(_ context.Context, callbackID string, text string) error {
	c.mu.Lock()
	c.answers = append(c.answers, callbackAnswer{id: callbackID, text: text})
	c.mu.Unlock()
	if c.events != nil {
		c.events.append("answer")
	}

	return nil
}

func (c *recordingCallbackClient) RemoveReplyMarkup(_ context.Context, chatID int64, messageID int) error {
	c.mu.Lock()
	c.removals = append(c.removals, removedReplyMarkup{chatID: chatID, messageID: messageID})
	c.mu.Unlock()
	if c.events != nil {
		c.events.append("remove")
	}

	return nil
}

func (c *recordingCallbackClient) answersSnapshot() []callbackAnswer {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]callbackAnswer(nil), c.answers...)
}

func (c *recordingCallbackClient) removalsSnapshot() []removedReplyMarkup {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]removedReplyMarkup(nil), c.removals...)
}

func (c *recordingCallbackClient) pollOffsetsSnapshot() []int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]int(nil), c.pollOffsets...)
}

type failingCallbackClient struct {
	err       error
	firstPoll chan struct{}
	polls     atomic.Int32
}

func (c *failingCallbackClient) PollCallbacks(context.Context, int) ([]telegram.Callback, int, error) {
	if c.polls.Add(1) == 1 {
		close(c.firstPoll)
	}

	return nil, 0, c.err
}

func (c *failingCallbackClient) AnswerCallback(context.Context, string, string) error {
	return nil
}

func (c *failingCallbackClient) RemoveReplyMarkup(context.Context, int64, int) error {
	return nil
}

type callbackAnswer struct {
	id   string
	text string
}

type removedReplyMarkup struct {
	chatID    int64
	messageID int
}

type recordedEvents struct {
	items []string
	mu    sync.Mutex
}

func (e *recordedEvents) append(item string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.items = append(e.items, item)
}

func (e *recordedEvents) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	return append([]string(nil), e.items...)
}

func waitForCallbackLoop(t *testing.T, done <-chan struct{}) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("callback loop did not stop")
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()

	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for signal")
	}
}
