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
	runner := newDigestRunner(app.Dependencies{
		Fetcher:      countingFetcher{calls: &fetches},
		Sender:       noopSender{},
		MessageLimit: 4096,
		Now:          time.Now,
	}, statePath, slog.New(slog.DiscardHandler))
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
	runner := newDigestRunner(app.Dependencies{
		Fetcher:      emptyFetcher{},
		Sender:       noopSender{},
		MessageLimit: 4096,
		Now:          time.Now,
	}, filepath.Join(t.TempDir(), "state.db"), slog.New(slog.DiscardHandler))

	// When
	done := startCallbackPolling(ctx, client, runner, slog.New(slog.DiscardHandler))
	waitForCallbackLoop(t, done)

	// Then
	require.Equal(t, []int{0, 101}, client.pollOffsets)
	require.Empty(t, client.answers)
	require.Empty(t, client.removals)
}

func Test_pollRefreshCallbacks_runs_digest_and_removes_old_button_before_new_send(t *testing.T) {
	t.Parallel()

	// Given
	ctx, cancel := context.WithCancel(context.Background())
	statePath := filepath.Join(t.TempDir(), "state.db")
	now := time.Date(2026, time.August, 8, 0, 0, 0, 0, time.UTC)
	book := alib.Book{Title: "Новая книга", BuyURL: "https://example.com/book"}
	events := make([]string, 0)
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
		events:     &events,
	}
	sender := &recordingSender{events: &events, afterSend: cancel}
	runner := newDigestRunner(app.Dependencies{
		Fetcher:      bookFetcher{books: []alib.Book{book}},
		Sender:       sender,
		MessageLimit: 4096,
		Now:          func() time.Time { return now },
	}, statePath, slog.New(slog.DiscardHandler))

	// When
	done := startCallbackPolling(ctx, client, runner, slog.New(slog.DiscardHandler))
	waitForCallbackLoop(t, done)
	state, err := store.Open(statePath, now)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, state.Close()) })
	pending, err := state.Pending(context.Background())

	// Then
	require.NoError(t, err)
	require.Empty(t, pending)
	require.Len(t, sender.messages, 1)
	require.Equal(t, []callbackAnswer{{id: "callback-1", text: refreshStartedText}}, client.answers)
	require.Equal(t, []removedReplyMarkup{{chatID: -100123, messageID: 77}}, client.removals)
	require.Equal(t, []string{"answer", "remove", "send"}, events)
}

func Test_handleRefreshCallback_leaves_old_button_when_digest_sends_no_books(t *testing.T) {
	t.Parallel()

	// Given
	statePath := filepath.Join(t.TempDir(), "state.db")
	now := time.Date(2026, time.August, 8, 0, 0, 0, 0, time.UTC)
	client := &recordingCallbackClient{}
	sender := &recordingSender{}
	runner := newDigestRunner(app.Dependencies{
		Fetcher:      emptyFetcher{},
		Sender:       sender,
		MessageLimit: 4096,
		Now:          func() time.Time { return now },
	}, statePath, slog.New(slog.DiscardHandler))

	// When
	handleRefreshCallback(context.Background(), client, runner, telegram.Callback{
		ID:            "callback-1",
		Data:          telegram.RefreshCallbackData,
		MessageChatID: -100123,
		MessageID:     77,
	}, slog.New(slog.DiscardHandler))

	// Then
	require.Equal(t, []callbackAnswer{{id: "callback-1", text: refreshStartedText}}, client.answers)
	require.Empty(t, client.removals)
	require.Empty(t, sender.messages)
}

func Test_handleRefreshCallback_answers_and_skips_when_digest_is_running(t *testing.T) {
	t.Parallel()

	// Given
	statePath := filepath.Join(t.TempDir(), "state.db")
	client := &recordingCallbackClient{}
	sender := &recordingSender{}
	runner := newDigestRunner(app.Dependencies{
		Fetcher:      emptyFetcher{},
		Sender:       sender,
		MessageLimit: 4096,
		Now:          time.Now,
	}, statePath, slog.New(slog.DiscardHandler))
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
	require.Equal(t, []callbackAnswer{{id: "callback-1", text: refreshAlreadyRunningText}}, client.answers)
	require.Empty(t, client.removals)
	require.Empty(t, sender.messages)
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

type noopSender struct{}

func (noopSender) Send(context.Context, string, bool, bool) error {
	return nil
}

type recordingSender struct {
	events        *[]string
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
		*s.events = append(*s.events, "send")
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
	events      *[]string
	cancel      context.CancelFunc
	callbacks   []telegram.Callback
	answers     []callbackAnswer
	removals    []removedReplyMarkup
	pollOffsets []int
	nextOffset  int
	polls       int
}

func (c *recordingCallbackClient) PollCallbacks(ctx context.Context, offset int) ([]telegram.Callback, int, error) {
	c.pollOffsets = append(c.pollOffsets, offset)
	if c.polls == 0 {
		c.polls++

		return c.callbacks, c.nextOffset, nil
	}
	if c.cancel != nil {
		c.cancel()
	}
	<-ctx.Done()

	return nil, offset, ctx.Err()
}

func (c *recordingCallbackClient) AnswerCallback(_ context.Context, callbackID string, text string) error {
	c.answers = append(c.answers, callbackAnswer{id: callbackID, text: text})
	if c.events != nil {
		*c.events = append(*c.events, "answer")
	}

	return nil
}

func (c *recordingCallbackClient) RemoveReplyMarkup(_ context.Context, chatID int64, messageID int) error {
	c.removals = append(c.removals, removedReplyMarkup{chatID: chatID, messageID: messageID})
	if c.events != nil {
		*c.events = append(*c.events, "remove")
	}

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

func waitForCallbackLoop(t *testing.T, done <-chan struct{}) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("callback loop did not stop")
	}
}
