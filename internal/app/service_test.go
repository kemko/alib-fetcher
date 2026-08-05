package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kemmko/alib-fetcher/internal/alib"
	"github.com/kemmko/alib-fetcher/internal/app"

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
	state := &fakeState{unseen: books}
	service := app.NewService(app.Dependencies{
		Fetcher:      fakeFetcher{books: books},
		State:        state,
		Sender:       &fakeSender{failAt: 2, err: sendErr},
		MessageLimit: 120,
		Now:          func() time.Time { return now },
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.ErrorIs(t, err, sendErr)
	require.Equal(t, 2, result.Fetched)
	require.Equal(t, 1, result.Sent)
	require.Equal(t, books[:1], state.marked)
	require.Equal(t, now, state.markedAt)
}

func Test_Service_does_not_send_seen_books(t *testing.T) {
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
	books []alib.Book
}

func (f fakeFetcher) Fetch(context.Context) ([]alib.Book, error) {
	return f.books, nil
}

type fakeState struct {
	markedAt     time.Time
	prunedBefore time.Time
	unseen       []alib.Book
	marked       []alib.Book
	pruned       int
}

func (f *fakeState) Prune(_ context.Context, before time.Time) (int, error) {
	f.prunedBefore = before
	return f.pruned, nil
}

func (f *fakeState) Unseen(context.Context, []alib.Book) ([]alib.Book, error) {
	return f.unseen, nil
}

func (f *fakeState) MarkSent(_ context.Context, books []alib.Book, sentAt time.Time) error {
	f.marked = append(f.marked, books...)
	f.markedAt = sentAt
	return nil
}

type fakeSender struct {
	err      error
	messages []string
	failAt   int
}

func (f *fakeSender) Send(_ context.Context, text string) error {
	f.messages = append(f.messages, text)
	if len(f.messages) == f.failAt {
		return f.err
	}
	return nil
}
