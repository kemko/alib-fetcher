package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kemmko/alib-fetcher/internal/alib"
	"github.com/kemmko/alib-fetcher/internal/app"

	"github.com/stretchr/testify/require"
)

func Test_Service_marks_each_chunk_only_after_delivery(t *testing.T) {
	t.Parallel()

	// Given
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
		MessageLimit: 100,
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.ErrorIs(t, err, sendErr)
	require.Equal(t, 2, result.Fetched)
	require.Equal(t, 1, result.Sent)
	require.Equal(t, books[:1], state.marked)
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
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.NoError(t, err)
	require.Equal(t, app.Result{Fetched: 1}, result)
	require.Empty(t, sender.messages)
}

type fakeFetcher struct {
	books []alib.Book
}

func (f fakeFetcher) Fetch(context.Context) ([]alib.Book, error) {
	return f.books, nil
}

type fakeState struct {
	unseen []alib.Book
	marked []alib.Book
}

func (f *fakeState) Unseen(context.Context, []alib.Book) ([]alib.Book, error) {
	return f.unseen, nil
}

func (f *fakeState) MarkSent(_ context.Context, books []alib.Book) error {
	f.marked = append(f.marked, books...)
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
