package app_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/app"
	"github.com/kemko/alib-fetcher/internal/digest"
	"github.com/kemko/alib-fetcher/internal/store"

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
		MessageLimit: 180,
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
	require.Equal(t, []bool{false, true}, sender.attachRefresh)
}

func Test_Service_sends_single_chunk_with_sound(t *testing.T) {
	t.Parallel()

	// Given
	book := alib.Book{
		Title:        "Книга",
		Bibliography: "Первая строка\r\nВторая строка",
		Content:      "Описание 1\rОписание 2\nОписание 3",
		BuyURL:       "https://example.com/1",
	}
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
	require.Equal(t, []bool{true}, sender.attachRefresh)
	require.Contains(t, sender.messages[0], "Первая строка<br/>Вторая строка")
	require.Contains(t, sender.messages[0], "Описание 1<br/>Описание 2<br/>Описание 3")
	require.NotContains(t, sender.messages[0], "\r")
	require.NotContains(t, sender.messages[0], "\n")
}

func Test_Service_renders_freshness_using_cycle_time_in_configured_timezone(t *testing.T) {
	t.Parallel()

	moscow, err := time.LoadLocation("Europe/Moscow")
	require.NoError(t, err)
	losAngeles, err := time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)
	testCases := map[string]struct {
		policy     app.FreshBooksPolicy
		location   *time.Location
		cycleTime  time.Time
		emoji      string
		bookYear   int
		policyYear int
	}{
		"disabled threshold": {
			cycleTime: time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC),
			location:  time.UTC,
			bookYear:  2025,
		},
		"inclusive configured threshold": {
			cycleTime: time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC),
			location:  time.UTC,
			bookYear:  2021,
			policy:    fixedFreshBooksPolicy(2021),
			emoji:     "✨ ",
		},
		"January boundary in configured timezone": {
			cycleTime:  time.Date(2025, time.December, 31, 21, 30, 0, 0, time.UTC),
			location:   moscow,
			bookYear:   2026,
			policyYear: 2026,
			emoji:      "🔥 ",
		},
		"future year uses configured timezone": {
			cycleTime:  time.Date(2027, time.January, 1, 0, 30, 0, 0, time.UTC),
			location:   losAngeles,
			bookYear:   2027,
			policyYear: 2026,
			emoji:      "🛸 ",
		},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// Given
			book := alib.Book{
				Title:           "Книга",
				PublicationYear: testCase.bookYear,
				BuyURL:          "https://example.com/book",
			}
			observedPolicyYear := 0
			policy := testCase.policy
			if testCase.policyYear != 0 {
				policy = freshBooksPolicyFunc(func(currentYear int) int {
					observedPolicyYear = currentYear

					return currentYear
				})
			}
			sender := &fakeSender{}
			service := app.NewService(app.Dependencies{
				Fetcher:      fakeFetcher{books: []alib.Book{book}},
				State:        &fakeState{pending: []alib.Book{book}, recordedNew: 1},
				Sender:       sender,
				FreshBooks:   policy,
				Location:     testCase.location,
				MessageLimit: 4096,
				Now:          func() time.Time { return testCase.cycleTime },
			})

			// When
			result, runErr := service.Run(context.Background())

			// Then
			require.NoError(t, runErr)
			require.Equal(t, app.Result{Fetched: 1, New: 1, Sent: 1}, result)
			require.Len(t, sender.messages, 1)
			require.Contains(t, sender.messages[0], testCase.emoji+"<b>Книга</b>")
			if testCase.policyYear != 0 {
				require.Equal(t, testCase.policyYear, observedPolicyYear)
			}
		})
	}
}

func Test_Service_sends_only_final_chunk_with_sound(t *testing.T) {
	t.Parallel()

	// Given
	now := time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)
	books := []alib.Book{
		{Title: "Первая", BuyURL: "https://example.com/1"},
		{Title: "Вторая", BuyURL: "https://example.com/2"},
		{Title: "Третья", BuyURL: "https://example.com/3"},
	}
	state := &fakeState{pending: books, recordedNew: len(books)}
	sender := &fakeSender{}
	service := app.NewService(app.Dependencies{
		Fetcher:      fakeFetcher{books: books},
		State:        state,
		Sender:       sender,
		MessageLimit: 170,
		Now:          func() time.Time { return now },
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.NoError(t, err)
	require.Equal(t, app.Result{Fetched: 3, New: 3, Sent: 3}, result)
	require.Len(t, sender.messages, 3)
	header := "Новые книги на Alib.ru"
	require.Contains(t, sender.messages[0], header)
	require.NotContains(t, sender.messages[1], header)
	require.NotContains(t, sender.messages[2], header)
	require.Equal(t, 1, strings.Count(strings.Join(sender.messages, ""), header))
	require.Contains(t, sender.messages[0], "Первая")
	require.NotContains(t, sender.messages[0], "Вторая")
	require.Contains(t, sender.messages[1], "Вторая")
	require.NotContains(t, sender.messages[1], "Третья")
	require.Contains(t, sender.messages[2], "Третья")
	require.Equal(t, books, state.marked)
	require.Equal(t, now, state.markedAt)
	require.Equal(t, []bool{true, true, false}, sender.silent)
	require.Equal(t, []bool{false, false, true}, sender.attachRefresh)
}

func Test_Service_sends_later_chunk_that_fits_only_without_header(t *testing.T) {
	t.Parallel()

	// Given
	books, messageLimit := headerlessOnlyLaterBookFixture(t)
	expectedChunks, err := digest.Render(books, digest.Options{Limit: messageLimit})
	require.NoError(t, err)
	require.Len(t, expectedChunks, 2)

	state := &fakeState{pending: books}
	sender := &fakeSender{}
	service := app.NewService(app.Dependencies{
		Fetcher:      fakeFetcher{},
		State:        state,
		Sender:       sender,
		MessageLimit: messageLimit,
		Now:          time.Now,
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.NoError(t, err)
	require.Equal(t, app.Result{Sent: 2}, result)
	require.Equal(t, []string{expectedChunks[0].Text, expectedChunks[1].Text}, sender.messages)
	require.Equal(t, books, state.marked)
}

func Test_Service_retries_headerless_only_chunk_after_partial_delivery_failure(t *testing.T) {
	t.Parallel()

	// Given
	now := time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)
	books, messageLimit := headerlessOnlyLaterBookFixture(t)
	expectedChunks, err := digest.Render(books, digest.Options{Limit: messageLimit})
	require.NoError(t, err)
	require.Len(t, expectedChunks, 2)

	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"), now)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, state.Close()) })
	sendErr := errors.New("telegram unavailable")
	sender := &fakeSender{failAt: 2, err: sendErr}
	service := app.NewService(app.Dependencies{
		Fetcher:      fakeFetcher{books: books},
		State:        state,
		Sender:       sender,
		MessageLimit: messageLimit,
		Now:          func() time.Time { return now },
	})

	// When
	firstResult, firstErr := service.Run(context.Background())
	pendingAfterFailure, pendingErr := state.Pending(context.Background())
	sender.failAt = 0
	secondResult, secondErr := service.Run(context.Background())
	pendingAfterRetry, retryPendingErr := state.Pending(context.Background())

	// Then
	require.ErrorIs(t, firstErr, sendErr)
	require.Equal(t, app.Result{Fetched: 2, New: 2, Sent: 1}, firstResult)
	require.NoError(t, pendingErr)
	require.Equal(t, books[1:], pendingAfterFailure)
	require.NoError(t, secondErr)
	require.Equal(t, app.Result{Fetched: 2, Sent: 1}, secondResult)
	require.NoError(t, retryPendingErr)
	require.Empty(t, pendingAfterRetry)
	header := `<p><b>Новые книги на Alib.ru</b></p>`
	require.Equal(t, []string{
		expectedChunks[0].Text,
		expectedChunks[1].Text,
		header,
		expectedChunks[1].Text,
	}, sender.messages)
	require.Equal(t, []bool{true, false, true, false}, sender.silent)
	require.Equal(t, []bool{false, true, false, true}, sender.attachRefresh)
}

func headerlessOnlyLaterBookFixture(t *testing.T) ([]alib.Book, int) {
	t.Helper()

	books := []alib.Book{
		{Title: "Первая", BuyURL: "https://example.com/1"},
		{Title: "Вторая длиннее первой", BuyURL: "https://example.com/2"},
	}
	firstBookChunks, err := digest.Render(books[:1], digest.Options{Limit: 4096})
	require.NoError(t, err)
	messageLimit := utf8.RuneCountInString(firstBookChunks[0].Text)
	secondBookChunks, err := digest.Render(books[1:], digest.Options{Limit: 4096})
	require.NoError(t, err)
	require.Greater(t, utf8.RuneCountInString(secondBookChunks[0].Text), messageLimit)

	return books, messageLimit
}

func Test_Service_runs_pre_delivery_hook_once_before_sending_and_marking(t *testing.T) {
	t.Parallel()

	// Given
	now := time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)
	book := alib.Book{Title: "Книга", BuyURL: "https://example.com/1"}
	events := make([]string, 0)
	state := &fakeState{pending: []alib.Book{book}, recordedNew: 1, events: &events}
	service := app.NewService(app.Dependencies{
		Fetcher: fakeFetcher{books: []alib.Book{book}, events: &events},
		State:   state,
		Sender:  &fakeSender{events: &events},
		BeforeDelivery: func(context.Context) error {
			events = append(events, "before_delivery")

			return nil
		},
		MessageLimit: 4096,
		Now:          func() time.Time { return now },
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.NoError(t, err)
	require.Equal(t, app.Result{Fetched: 1, New: 1, Sent: 1}, result)
	require.Equal(t, []string{
		"fetch",
		"record",
		"pending",
		"before_delivery",
		"send",
		"mark:https://example.com/1",
	}, events)
}

func Test_Service_does_not_send_or_mark_when_pre_delivery_hook_fails(t *testing.T) {
	t.Parallel()

	// Given
	hookErr := errors.New("remove refresh button")
	now := time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)
	book := alib.Book{Title: "Книга", BuyURL: "https://example.com/1"}
	state := &fakeState{pending: []alib.Book{book}, recordedNew: 1}
	sender := &fakeSender{}
	service := app.NewService(app.Dependencies{
		Fetcher: fakeFetcher{books: []alib.Book{book}},
		State:   state,
		Sender:  sender,
		BeforeDelivery: func(context.Context) error {
			return hookErr
		},
		MessageLimit: 4096,
		Now:          func() time.Time { return now },
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.ErrorIs(t, err, hookErr)
	require.Equal(t, app.Result{Fetched: 1, New: 1}, result)
	require.Empty(t, sender.messages)
	require.Empty(t, state.marked)
}

func Test_Service_waits_and_retries_only_rate_limited_chunk(t *testing.T) {
	t.Parallel()

	// Given
	now := time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)
	books := []alib.Book{
		{Title: "Первая", BuyURL: "https://example.com/1"},
		{Title: "Вторая", BuyURL: "https://example.com/2"},
		{Title: "Третья", BuyURL: "https://example.com/3"},
	}
	events := make([]string, 0)
	state := &fakeState{pending: books, recordedNew: len(books), events: &events}
	sender := &fakeSender{
		failAt: 3,
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
		BeforeDelivery: func(context.Context) error {
			events = append(events, "before_delivery")

			return nil
		},
		MessageLimit: 170,
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.NoError(t, err)
	require.Equal(t, 3, result.Sent)
	require.Equal(t, books, state.marked)
	require.Len(t, sender.messages, 4)
	require.NotEqual(t, sender.messages[0], sender.messages[1])
	require.NotEqual(t, sender.messages[1], sender.messages[2])
	require.Equal(t, sender.messages[2], sender.messages[3])
	require.Equal(t, []bool{true, true, false, false}, sender.silent)
	require.Equal(t, []bool{false, false, true, true}, sender.attachRefresh)
	require.Equal(t, []string{
		"record",
		"pending",
		"before_delivery",
		"send",
		"mark:https://example.com/1",
		"send",
		"mark:https://example.com/2",
		"send",
		"wait",
		"send",
		"mark:https://example.com/3",
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
	nowCalls := 0
	service := app.NewService(app.Dependencies{
		Fetcher:      fakeFetcher{books: books, events: &events},
		State:        state,
		Sender:       &fakeSender{events: &events},
		MessageLimit: 4096,
		Now: func() time.Time {
			nowCalls++

			return now
		},
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.NoError(t, err)
	require.Equal(t, 1, nowCalls)
	require.Equal(t, now.Add(-14*24*time.Hour), state.prunedBefore)
	require.Equal(t, books, state.recorded)
	require.Equal(t, now, state.recordedAt)
	require.Equal(t, now, state.markedAt)
	require.Equal(t, app.Result{Fetched: 1, New: 1, Sent: 1}, result)
	require.Equal(t, []string{
		"fetch",
		"record",
		"pending",
		"send",
		"mark:https://example.com/new",
	}, events)
}

func Test_Service_stops_when_record_discovered_fails(t *testing.T) {
	t.Parallel()

	// Given
	recordErr := errors.New("database write failed")
	book := alib.Book{Title: "Новая", BuyURL: "https://example.com/new"}
	events := make([]string, 0)
	state := &fakeState{events: &events, recordErr: recordErr}
	sender := &fakeSender{}
	service := app.NewService(app.Dependencies{
		Fetcher:      fakeFetcher{books: []alib.Book{book}},
		State:        state,
		Sender:       sender,
		MessageLimit: 4096,
		Now:          time.Now,
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.ErrorIs(t, err, recordErr)
	require.Contains(t, err.Error(), "record discovered listings")
	require.Equal(t, app.Result{Fetched: 1}, result)
	require.Equal(t, []string{"record"}, events)
	require.Empty(t, sender.messages)
	require.Empty(t, state.marked)
}

func Test_Service_stops_when_pending_load_fails(t *testing.T) {
	t.Parallel()

	// Given
	pendingErr := errors.New("database read failed")
	book := alib.Book{Title: "Новая", BuyURL: "https://example.com/new"}
	events := make([]string, 0)
	state := &fakeState{events: &events, recordedNew: 1, pendingErr: pendingErr}
	sender := &fakeSender{}
	service := app.NewService(app.Dependencies{
		Fetcher:      fakeFetcher{books: []alib.Book{book}},
		State:        state,
		Sender:       sender,
		MessageLimit: 4096,
		Now:          time.Now,
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.ErrorIs(t, err, pendingErr)
	require.Contains(t, err.Error(), "load pending listings")
	require.Equal(t, app.Result{Fetched: 1, New: 1}, result)
	require.Equal(t, []string{"record", "pending"}, events)
	require.Empty(t, sender.messages)
	require.Empty(t, state.marked)
}

func Test_Service_sends_pending_books_not_present_in_current_fetch(t *testing.T) {
	t.Parallel()

	// Given
	now := time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"), now)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, state.Close()) })
	fetched := []alib.Book{{Title: "Свежая", BuyURL: "https://example.com/2-fresh"}}
	previous := []alib.Book{{Title: "Из прошлой попытки", BuyURL: "https://example.com/1-pending"}}
	_, err = state.RecordDiscovered(context.Background(), previous, now.Add(-time.Hour))
	require.NoError(t, err)
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
	remaining, pendingErr := state.Pending(context.Background())

	// Then
	require.NoError(t, err)
	require.NoError(t, pendingErr)
	require.Empty(t, remaining)
	require.Equal(t, app.Result{Fetched: 1, New: 1, Sent: 2}, result)
	require.Len(t, sender.messages, 1)
	require.Contains(t, sender.messages[0], "Из прошлой попытки")
	require.Contains(t, sender.messages[0], "Свежая")
}

func Test_Service_marks_sent_after_delivery_even_if_job_context_is_canceled(t *testing.T) {
	t.Parallel()

	// Given
	now := time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)
	book := alib.Book{Title: "Книга", BuyURL: "https://example.com/1"}
	state := &fakeState{pending: []alib.Book{book}, recordedNew: 1}
	ctx, cancel := context.WithCancel(context.Background())
	sender := &fakeSender{afterSend: cancel}
	service := app.NewService(app.Dependencies{
		Fetcher:      fakeFetcher{books: []alib.Book{book}},
		State:        state,
		Sender:       sender,
		MessageLimit: 4096,
		Now:          func() time.Time { return now },
	})

	// When
	result, err := service.Run(ctx)

	// Then
	require.NoError(t, err)
	require.Equal(t, app.Result{Fetched: 1, New: 1, Sent: 1}, result)
	require.Equal(t, []alib.Book{book}, state.marked)
	require.Equal(t, now, state.markedAt)
}

func Test_Service_sends_renderable_pending_books_when_one_pending_book_is_too_long(t *testing.T) {
	t.Parallel()

	// Given
	now := time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)
	oversized := alib.Book{Title: strings.Repeat("Очень длинная книга ", 20), BuyURL: "https://e/oversized"}
	firstDeliverable := alib.Book{Title: "Обычная 1", BuyURL: "https://e/1"}
	secondDeliverable := alib.Book{Title: "Обычная 2", BuyURL: "https://e/2"}
	firstChunks, err := digest.Render([]alib.Book{firstDeliverable}, digest.Options{Limit: 4096})
	require.NoError(t, err)
	messageLimit := utf8.RuneCountInString(firstChunks[0].Text)
	state := &fakeState{pending: []alib.Book{oversized, firstDeliverable, secondDeliverable}}
	sender := &fakeSender{}
	hookCalls := 0
	service := app.NewService(app.Dependencies{
		Fetcher: fakeFetcher{},
		State:   state,
		Sender:  sender,
		BeforeDelivery: func(context.Context) error {
			hookCalls++

			return nil
		},
		MessageLimit: messageLimit,
		Now:          func() time.Time { return now },
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.ErrorIs(t, err, digest.ErrMessageTooLong)
	require.Equal(t, app.Result{Sent: 2}, result)
	require.Equal(t, []alib.Book{firstDeliverable, secondDeliverable}, state.marked)
	require.Len(t, sender.messages, 2)
	require.Contains(t, sender.messages[0], "Обычная")
	require.NotContains(t, strings.Join(sender.messages, "\n"), "Очень длинная книга")
	require.Equal(t, []bool{false, true}, sender.attachRefresh)
	require.Equal(t, []bool{true, false}, sender.silent)
	require.Equal(t, 1, hookCalls)
}

func Test_Service_does_not_run_pre_delivery_hook_when_no_chunks_are_renderable(t *testing.T) {
	t.Parallel()

	// Given
	oversized := alib.Book{Title: strings.Repeat("Очень длинная книга ", 20), BuyURL: "https://example.com/oversized"}
	hookCalls := 0
	state := &fakeState{pending: []alib.Book{oversized}}
	sender := &fakeSender{}
	service := app.NewService(app.Dependencies{
		Fetcher: fakeFetcher{},
		State:   state,
		Sender:  sender,
		BeforeDelivery: func(context.Context) error {
			hookCalls++

			return nil
		},
		MessageLimit: 120,
		Now:          time.Now,
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.ErrorIs(t, err, digest.ErrMessageTooLong)
	require.Empty(t, result.Sent)
	require.Zero(t, hookCalls)
	require.Empty(t, sender.messages)
	require.Empty(t, state.marked)
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
	recordErr    error
	pendingErr   error
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
	if f.recordErr != nil {
		return 0, f.recordErr
	}
	return f.recordedNew, nil
}

func (f *fakeState) Pending(context.Context) ([]alib.Book, error) {
	if f.events != nil {
		*f.events = append(*f.events, "pending")
	}
	if f.pendingErr != nil {
		return nil, f.pendingErr
	}
	return f.pending, nil
}

func (f *fakeState) MarkSent(ctx context.Context, books []alib.Book, sentAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
	events        *[]string
	err           error
	afterSend     func()
	messages      []string
	silent        []bool
	attachRefresh []bool
	failAt        int
}

func (f *fakeSender) Send(_ context.Context, text string, silent bool, attachRefresh bool) error {
	f.messages = append(f.messages, text)
	f.silent = append(f.silent, silent)
	f.attachRefresh = append(f.attachRefresh, attachRefresh)
	if f.events != nil {
		*f.events = append(*f.events, "send")
	}
	if f.afterSend != nil {
		f.afterSend()
	}
	if len(f.messages) == f.failAt {
		return f.err
	}
	return nil
}

type retryAfterError struct {
	delay time.Duration
}

type fixedFreshBooksPolicy int

func (policy fixedFreshBooksPolicy) LowerYear(int) int {
	return int(policy)
}

type freshBooksPolicyFunc func(int) int

func (policy freshBooksPolicyFunc) LowerYear(currentYear int) int {
	return policy(currentYear)
}

func (e retryAfterError) Error() string {
	return "rate limited"
}

func (e retryAfterError) RetryAfter() time.Duration {
	return e.delay
}
