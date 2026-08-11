package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/app"

	"github.com/stretchr/testify/require"
)

func Test_Service_marks_each_chunk_only_after_delivery(t *testing.T) {
	t.Parallel()

	// Given
	now := time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)
	books := []alib.Book{
		{Title: "Первая", BuyURL: "https://example.com/1"},
		{Title: "Вторая", BuyURL: "https://example.com/2"},
	}
	sendErr := errors.New("telegram unavailable")
	state := &fakeState{pending: books, recordedNew: len(books)}
	sender := &fakeSender{failAt: 2, err: sendErr}
	service := app.NewService(app.Dependencies{
		Fetcher:      fakeFetcher{books: books},
		State:        state,
		Sender:       sender,
		MessageLimit: 120,
		Now:          func() time.Time { return now },
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.ErrorIs(t, err, sendErr)
	require.Equal(t, 2, result.Fetched)
	require.Equal(t, 2, result.New)
	require.Equal(t, 1, result.Sent)
	require.Equal(t, books[:1], state.marked)
	require.Equal(t, now, state.markedAt)
	require.Equal(t, []bool{true, false}, sender.silent)
}

func Test_Service_sends_single_chunk_with_sound(t *testing.T) {
	t.Parallel()

	// Given
	book := alib.Book{Title: "Книга", BuyURL: "https://example.com/1"}
	sender := &fakeSender{}
	service := app.NewService(app.Dependencies{
		Fetcher:      fakeFetcher{books: []alib.Book{book}},
		State:        &fakeState{pending: []alib.Book{book}, recordedNew: 1},
		Sender:       sender,
		MessageLimit: 4096,
		Now:          time.Now,
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.NoError(t, err)
	require.Equal(t, 1, result.New)
	require.Equal(t, 1, result.Sent)
	require.Equal(t, []bool{false}, sender.silent)
}

func Test_Service_waits_and_retries_only_rate_limited_chunk(t *testing.T) {
	t.Parallel()

	// Given
	now := time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)
	books := []alib.Book{
		{Title: "Первая", BuyURL: "https://example.com/1"},
		{Title: "Вторая", BuyURL: "https://example.com/2"},
	}
	events := make([]string, 0)
	state := &fakeState{pending: books, recordedNew: len(books), events: &events}
	sender := &fakeSender{
		failAt: 2,
		err:    retryAfterError{delay: time.Second},
		events: &events,
	}
	service := app.NewService(app.Dependencies{
		Fetcher: fakeFetcher{books: books},
		State:   state,
		Sender:  sender,
		Now:     func() time.Time { return now },
		Wait: func(_ context.Context, delay time.Duration) error {
			events = append(events, "wait")
			require.Equal(t, time.Second, delay)
			return nil
		},
		MessageLimit: 120,
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.NoError(t, err)
	require.Equal(t, 2, result.Sent)
	require.Equal(t, books, state.marked)
	require.Len(t, sender.messages, 3)
	require.NotEqual(t, sender.messages[0], sender.messages[1])
	require.Equal(t, sender.messages[1], sender.messages[2])
	require.Equal(t, []bool{true, false, false}, sender.silent)
	require.Equal(t, []string{
		"record",
		"pending",
		"send",
		"mark:https://example.com/1",
		"send",
		"wait",
		"send",
		"mark:https://example.com/2",
	}, events)
}

func Test_Service_stops_rate_limit_wait_when_context_is_canceled(t *testing.T) {
	t.Parallel()

	// Given
	book := alib.Book{Title: "Книга", BuyURL: "https://example.com/1"}
	state := &fakeState{pending: []alib.Book{book}, recordedNew: 1}
	sender := &fakeSender{failAt: 1, err: retryAfterError{delay: time.Second}}
	service := app.NewService(app.Dependencies{
		Fetcher:      fakeFetcher{books: []alib.Book{book}},
		State:        state,
		Sender:       sender,
		Now:          time.Now,
		MessageLimit: 4096,
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// When
	result, err := service.Run(ctx)

	// Then
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, result.Sent)
	require.Empty(t, state.marked)
	require.Len(t, sender.messages, 1)
}

func Test_Service_records_discovered_before_loading_pending_and_sending(t *testing.T) {
	t.Parallel()

	// Given
	now := time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)
	books := []alib.Book{{Title: "Новая", BuyURL: "https://example.com/new"}}
	events := make([]string, 0)
	state := &fakeState{pending: books, recordedNew: 1, events: &events}
	service := app.NewService(app.Dependencies{
		Fetcher:      fakeFetcher{books: books, events: &events},
		State:        state,
		Sender:       &fakeSender{events: &events},
		MessageLimit: 4096,
		Now:          func() time.Time { return now },
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.NoError(t, err)
	require.Equal(t, books, state.recorded)
	require.Equal(t, now, state.recordedAt)
	require.Equal(t, app.Result{Fetched: 1, New: 1, Sent: 1}, result)
	require.Equal(t, []string{
		"fetch",
		"record",
		"pending",
		"send",
		"mark:https://example.com/new",
	}, events)
}

func Test_Service_sends_pending_books_not_present_in_current_fetch(t *testing.T) {
	t.Parallel()

	// Given
	now := time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)
	fetched := []alib.Book{{Title: "Свежая", BuyURL: "https://example.com/fresh"}}
	pending := []alib.Book{{Title: "Из прошлой попытки", BuyURL: "https://example.com/pending"}}
	state := &fakeState{pending: pending, recordedNew: 1}
	sender := &fakeSender{}
	service := app.NewService(app.Dependencies{
		Fetcher:      fakeFetcher{books: fetched},
		State:        state,
		Sender:       sender,
		MessageLimit: 4096,
		Now:          func() time.Time { return now },
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.NoError(t, err)
	require.Equal(t, fetched, state.recorded)
	require.Equal(t, pending, state.marked)
	require.Equal(t, now, state.markedAt)
	require.Equal(t, app.Result{Fetched: 1, New: 1, Sent: 1}, result)
	require.Len(t, sender.messages, 1)
	require.Contains(t, sender.messages[0], "Из прошлой попытки")
	require.NotContains(t, sender.messages[0], "Свежая")
}

func Test_Service_does_not_send_when_no_pending_books(t *testing.T) {
	t.Parallel()

	// Given
	book := alib.Book{Title: "Уже отправлена", BuyURL: "https://example.com/1"}
	sender := &fakeSender{}
	service := app.NewService(app.Dependencies{
		Fetcher:      fakeFetcher{books: []alib.Book{book}},
		State:        &fakeState{},
		Sender:       sender,
		MessageLimit: 4096,
		Now:          time.Now,
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.NoError(t, err)
	require.Equal(t, app.Result{Fetched: 1}, result)
	require.Empty(t, sender.messages)
}

func Test_Service_prunes_state_at_start_of_cycle(t *testing.T) {
	t.Parallel()

	// Given
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	state := &fakeState{pruned: 3}
	service := app.NewService(app.Dependencies{
		Fetcher:      fakeFetcher{},
		State:        state,
		Sender:       &fakeSender{},
		MessageLimit: 4096,
		Now:          func() time.Time { return now },
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.NoError(t, err)
	require.Equal(t, now.Add(-14*24*time.Hour), state.prunedBefore)
	require.Equal(t, 3, result.Pruned)
}

type fakeFetcher struct {
	events *[]string
	books  []alib.Book
}

func (f fakeFetcher) Fetch(context.Context) ([]alib.Book, error) {
	if f.events != nil {
		*f.events = append(*f.events, "fetch")
	}
	return f.books, nil
}

type fakeState struct {
	events       *[]string
	markedAt     time.Time
	prunedBefore time.Time
	recordedAt   time.Time
	pending      []alib.Book
	marked       []alib.Book
	recorded     []alib.Book
	recordedNew  int
	pruned       int
}

func (f *fakeState) Prune(_ context.Context, before time.Time) (int, error) {
	f.prunedBefore = before
	return f.pruned, nil
}

func (f *fakeState) RecordDiscovered(_ context.Context, books []alib.Book, observedAt time.Time) (int, error) {
	f.recorded = append(f.recorded, books...)
	f.recordedAt = observedAt
	if f.events != nil {
		*f.events = append(*f.events, "record")
	}
	return f.recordedNew, nil
}

func (f *fakeState) Pending(context.Context) ([]alib.Book, error) {
	if f.events != nil {
		*f.events = append(*f.events, "pending")
	}
	return f.pending, nil
}

func (f *fakeState) MarkSent(_ context.Context, books []alib.Book, sentAt time.Time) error {
	f.marked = append(f.marked, books...)
	f.markedAt = sentAt
	if f.events != nil {
		for _, book := range books {
			*f.events = append(*f.events, "mark:"+book.BuyURL)
		}
	}
	return nil
}

type fakeSender struct {
	events   *[]string
	err      error
	messages []string
	silent   []bool
	failAt   int
}

func (f *fakeSender) Send(_ context.Context, text string, silent bool) error {
	f.messages = append(f.messages, text)
	f.silent = append(f.silent, silent)
	if f.events != nil {
		*f.events = append(*f.events, "send")
	}
	if len(f.messages) == f.failAt {
		return f.err
	}
	return nil
}

type retryAfterError struct {
	delay time.Duration
}

func (e retryAfterError) Error() string {
	return "rate limited"
}

func (e retryAfterError) RetryAfter() time.Duration {
	return e.delay
}
