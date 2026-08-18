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
	"github.com/kemko/alib-fetcher/internal/telegram"

	"github.com/stretchr/testify/require"
)

func Test_startCallbackListening_ignores_unknown_callback_data(t *testing.T) {
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
		cancel: cancel,
	}
	runner := &digestRunner{dependencies: app.Dependencies{
		Fetcher:      emptyFetcher{},
		Sender:       noopSender{},
		MessageLimit: 4096,
		Now:          time.Now,
	}, statePath: filepath.Join(t.TempDir(), "state.db"), logger: slog.New(slog.DiscardHandler)}

	// When
	done := startCallbackListening(ctx, client, runner, "-100123", slog.New(slog.DiscardHandler))
	waitForCallbackLoop(t, done)

	// Then
	require.Equal(t, int32(1), client.listens.Load())
	require.Empty(t, client.answersSnapshot())
	require.Empty(t, client.removalsSnapshot())
}

func Test_startCallbackListening_runs_digest_and_removes_old_button_before_new_send(t *testing.T) {
	t.Parallel()

	// Given
	ctx, cancel := context.WithCancel(context.Background())
	statePath := filepath.Join(t.TempDir(), "state.db")
	now := time.Date(2026, time.August, 8, 0, 0, 0, 0, time.UTC)
	book := alib.Book{Title: "Новая книга", BuyURL: "https://example.com/book"}
	events := &recordedEvents{}
	sent := make(chan struct{})
	client := &recordingCallbackClient{
		callbacks: []telegram.Callback{
			{
				ID:                  "callback-1",
				Data:                telegram.RefreshCallbackData,
				MessageChatUsername: "Books",
				MessageChatID:       -100123,
				MessageID:           77,
			},
		},
		events: events,
	}
	sender := &recordingSender{events: events, afterSend: func() { close(sent) }}
	runner := &digestRunner{dependencies: app.Dependencies{
		Fetcher:      bookFetcher{books: []alib.Book{book}},
		Sender:       sender,
		MessageLimit: 4096,
		Now:          func() time.Time { return now },
	}, statePath: statePath, logger: slog.New(slog.DiscardHandler)}

	// When
	done := startCallbackListening(ctx, client, runner, "@books", slog.New(slog.DiscardHandler))
	waitForSignal(t, sent)
	runner.wait()
	cancel()
	waitForCallbackLoop(t, done)
	state, err := store.Open(statePath, now)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, state.Close()) })
	pending, err := state.Pending(context.Background())

	// Then
	require.NoError(t, err)
	require.Empty(t, pending)
	require.Len(t, sender.messages, 1)
	require.Equal(t, []callbackAnswer{{id: "callback-1", text: ""}}, client.answersSnapshot())
	require.Equal(t, []removedReplyMarkup{{chatID: -100123, messageID: 77}}, client.removalsSnapshot())
	require.Equal(t, []string{"remove", "send", "answer"}, events.snapshot())
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
	runner.wait()

	// Then
	require.Equal(t, []callbackAnswer{{id: "callback-1", text: refreshNoBooksText}}, client.answersSnapshot())
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
	digestStarted := make(chan struct{})
	releaseDigest := make(chan struct{})
	client := &recordingCallbackClient{}
	runner := newDigestRunner(app.Dependencies{
		Fetcher:      &blockingFetcher{started: digestStarted, release: releaseDigest},
		Sender:       noopSender{},
		MessageLimit: 4096,
		Now:          time.Now,
	}, filepath.Join(t.TempDir(), "state.db"), slog.New(slog.DiscardHandler))

	// When
	handleRefreshCallback(ctx, client, runner, telegram.Callback{
		ID:            "callback-1",
		Data:          telegram.RefreshCallbackData,
		MessageChatID: -100123,
		MessageID:     77,
	}, slog.New(slog.DiscardHandler))
	waitForSignal(t, digestStarted)
	require.Empty(t, client.answersSnapshot())
	handleRefreshCallback(ctx, client, runner, telegram.Callback{
		ID:            "callback-2",
		Data:          telegram.RefreshCallbackData,
		MessageChatID: -100123,
		MessageID:     77,
	}, slog.New(slog.DiscardHandler))
	close(releaseDigest)
	runner.wait()

	// Then
	require.Equal(t, []callbackAnswer{
		{id: "callback-2", text: refreshAlreadyRunningText},
		{id: "callback-1", text: refreshNoBooksText},
	}, client.answersSnapshot())
}

func Test_handleRefreshCallback_hides_digest_error_details_after_digest_finishes(t *testing.T) {
	t.Parallel()

	// Given
	digestErr := errors.New(`fetch https://user:secret@example.com/listings: failed`)
	client := &recordingCallbackClient{}
	runner := &digestRunner{dependencies: app.Dependencies{
		Fetcher:      errorFetcher{err: digestErr},
		Sender:       noopSender{},
		MessageLimit: 4096,
		Now:          time.Now,
	}, statePath: filepath.Join(t.TempDir(), "state.db"), logger: slog.New(slog.DiscardHandler)}

	// When
	handleRefreshCallback(context.Background(), client, runner, telegram.Callback{
		ID:            "callback-1",
		Data:          telegram.RefreshCallbackData,
		MessageChatID: -100123,
		MessageID:     77,
	}, slog.New(slog.DiscardHandler))
	runner.wait()

	// Then
	require.Equal(t, []callbackAnswer{{id: "callback-1", text: refreshFailedText}}, client.answersSnapshot())
	require.Empty(t, client.removalsSnapshot())
}

func Test_handleRefreshCallback_times_out_before_callback_expires(t *testing.T) {
	t.Parallel()

	// Given
	digestStarted := make(chan struct{})
	client := &recordingCallbackClient{}
	runner := &digestRunner{dependencies: app.Dependencies{
		Fetcher:      &blockingFetcher{started: digestStarted},
		Sender:       noopSender{},
		MessageLimit: 4096,
		Now:          time.Now,
	}, statePath: filepath.Join(t.TempDir(), "state.db"), logger: slog.New(slog.DiscardHandler)}

	// When
	handleRefreshCallbackWithin(context.Background(), client, runner, telegram.Callback{
		ID:            "callback-1",
		Data:          telegram.RefreshCallbackData,
		MessageChatID: -100123,
		MessageID:     77,
	}, slog.New(slog.DiscardHandler), 250*time.Millisecond)
	waitForSignal(t, digestStarted)
	runner.wait()

	// Then
	require.Equal(t, []callbackAnswer{{id: "callback-1", text: refreshFailedText}}, client.answersSnapshot())
	require.Empty(t, client.removalsSnapshot())
}

func Test_handleRefreshCallback_prefers_error_status_after_discovering_new_book(t *testing.T) {
	t.Parallel()

	// Given
	book := alib.Book{Title: "Книга", BuyURL: "https://example.com/book"}
	client := &recordingCallbackClient{}
	sender := &recordingSender{err: errors.New("send failed")}
	runner := &digestRunner{dependencies: app.Dependencies{
		Fetcher:      bookFetcher{books: []alib.Book{book}},
		Sender:       sender,
		MessageLimit: 4096,
		Now:          time.Now,
	}, statePath: filepath.Join(t.TempDir(), "state.db"), logger: slog.New(slog.DiscardHandler)}

	// When
	handleRefreshCallback(context.Background(), client, runner, telegram.Callback{
		ID:            "callback-1",
		Data:          telegram.RefreshCallbackData,
		MessageChatID: -100123,
		MessageID:     77,
	}, slog.New(slog.DiscardHandler))
	runner.wait()

	// Then
	require.Equal(t, []callbackAnswer{{id: "callback-1", text: refreshFailedText}}, client.answersSnapshot())
	require.Len(t, sender.messages, 1)
}

func Test_handleRefreshCallback_logs_final_answer_failure(t *testing.T) {
	t.Parallel()

	// Given
	answerErr := errors.New("callback answer failed")
	var logs bytes.Buffer
	client := &recordingCallbackClient{answerErr: answerErr}
	runner := &digestRunner{dependencies: app.Dependencies{
		Fetcher:      emptyFetcher{},
		Sender:       noopSender{},
		MessageLimit: 4096,
		Now:          time.Now,
	}, statePath: filepath.Join(t.TempDir(), "state.db"), logger: slog.New(slog.DiscardHandler)}

	// When
	handleRefreshCallback(context.Background(), client, runner, telegram.Callback{
		ID:            "callback-1",
		Data:          telegram.RefreshCallbackData,
		MessageChatID: -100123,
		MessageID:     77,
	}, slog.New(slog.NewTextHandler(&logs, nil)))
	runner.wait()

	// Then
	require.Contains(t, logs.String(), "msg=callback.answer_failed")
	require.Contains(t, logs.String(), "callback answer failed")
}

func Test_startCallbackListening_answers_and_ignores_refresh_from_unexpected_chat(t *testing.T) {
	t.Parallel()

	// Given
	ctx, cancel := context.WithCancel(context.Background())
	var fetches atomic.Int32
	client := &recordingCallbackClient{
		callbacks: []telegram.Callback{
			{
				ID:            "callback-1",
				Data:          telegram.RefreshCallbackData,
				MessageChatID: -100999,
				MessageID:     77,
			},
		},
		cancel: cancel,
	}
	runner := &digestRunner{dependencies: app.Dependencies{
		Fetcher:      countingFetcher{calls: &fetches},
		Sender:       noopSender{},
		MessageLimit: 4096,
		Now:          time.Now,
	}, statePath: filepath.Join(t.TempDir(), "state.db"), logger: slog.New(slog.DiscardHandler)}

	// When
	done := startCallbackListening(ctx, client, runner, "-100123", slog.New(slog.DiscardHandler))
	waitForCallbackLoop(t, done)
	runner.wait()

	// Then
	require.Equal(t, int32(1), client.listens.Load())
	require.Equal(t, []callbackAnswer{{id: "callback-1", text: refreshUnavailableText}}, client.answersSnapshot())
	require.Empty(t, client.removalsSnapshot())
	require.Zero(t, fetches.Load())
}

func Test_startCallbackListening_logs_listener_error_without_process_retry(t *testing.T) {
	t.Parallel()

	// Given
	ctx, cancel := context.WithCancel(context.Background())
	var logs bytes.Buffer
	client := &recordingCallbackClient{
		listenErr: errors.New("telegram unavailable"),
		cancel:    cancel,
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
	done := startCallbackListening(ctx, client, runner, "", slog.New(slog.NewTextHandler(&logs, nil)))
	waitForCallbackLoop(t, done)

	// Then
	require.Equal(t, int32(1), client.listens.Load())
	require.Contains(t, logs.String(), "msg=callback.poll_failed")
	require.Contains(t, logs.String(), "telegram unavailable")
	require.NotContains(t, logs.String(), "update_offset")
}

type recordingSender struct {
	events        *recordedEvents
	afterSend     func()
	err           error
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

	return s.err
}

type recordingCallbackClient struct {
	events    *recordedEvents
	cancel    context.CancelFunc
	listenErr error
	answerErr error
	callbacks []telegram.Callback
	answers   []callbackAnswer
	removals  []removedReplyMarkup
	listens   atomic.Int32
	mu        sync.Mutex
}

func (c *recordingCallbackClient) ListenCallbacks(
	ctx context.Context,
	handle telegram.CallbackHandler,
	reportError telegram.CallbackErrorHandler,
) {
	c.listens.Add(1)
	for _, callback := range c.callbacks {
		handle(ctx, callback)
	}
	if c.listenErr != nil {
		reportError(ctx, c.listenErr)
	}
	if c.cancel != nil {
		c.cancel()
	}
	<-ctx.Done()
}

func (c *recordingCallbackClient) AnswerCallback(ctx context.Context, callbackID string, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.answers = append(c.answers, callbackAnswer{id: callbackID, text: text})
	c.mu.Unlock()
	if c.events != nil {
		c.events.append("answer")
	}

	return c.answerErr
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
