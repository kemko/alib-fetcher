package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/kemmko/alib-fetcher/internal/alib"
	"github.com/kemmko/alib-fetcher/internal/store"
	"github.com/stretchr/testify/require"
)

func Test_Store_returns_only_books_not_marked_as_sent(t *testing.T) {
	t.Parallel()

	// Given
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	books := []alib.Book{{BuyURL: "https://example.com/1"}, {BuyURL: "https://example.com/2"}}

	// When
	require.NoError(t, db.MarkSent(context.Background(), books[:1]))
	unseen, err := db.Unseen(context.Background(), books)

	// Then
	require.NoError(t, err)
	require.Equal(t, books[1:], unseen)
}

func Test_Open_creates_parent_directory(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "nested", "state.db")

	// When
	db, err := store.Open(path)

	// Then
	require.NoError(t, err)
	require.NoError(t, db.Close())
}
