package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/kemmko/alib-fetcher/internal/alib"
	"github.com/kemmko/alib-fetcher/internal/store"

	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

func Test_Store_returns_only_books_not_marked_as_sent(t *testing.T) {
	t.Parallel()

	// Given
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"), time.Now())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	books := []alib.Book{{BuyURL: "https://example.com/1"}, {BuyURL: "https://example.com/2"}}

	// When
	require.NoError(t, db.MarkSent(context.Background(), books[:1], time.Now()))
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
	db, err := store.Open(path, time.Now())

	// Then
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

func Test_Store_prunes_only_entries_older_than_cutoff(t *testing.T) {
	t.Parallel()

	// Given
	now := time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"), now)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	oldBook := alib.Book{BuyURL: "https://example.com/old"}
	boundaryBook := alib.Book{BuyURL: "https://example.com/boundary"}
	require.NoError(t, db.MarkSent(context.Background(), []alib.Book{oldBook}, now.Add(-15*24*time.Hour)))
	require.NoError(t, db.MarkSent(context.Background(), []alib.Book{boundaryBook}, now.Add(-14*24*time.Hour)))

	// When
	pruned, err := db.Prune(context.Background(), now.Add(-14*24*time.Hour))
	unseen, unseenErr := db.Unseen(context.Background(), []alib.Book{oldBook, boundaryBook})

	// Then
	require.NoError(t, err)
	require.NoError(t, unseenErr)
	require.Equal(t, 1, pruned)
	require.Equal(t, []alib.Book{oldBook}, unseen)
}

func Test_Open_migrates_legacy_markers_without_immediate_pruning(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	book := alib.Book{BuyURL: "https://example.com/legacy"}
	require.NoError(t, writeLegacyMarker(path, book.BuyURL))
	migratedAt := time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)

	// When
	db, err := store.Open(path, migratedAt)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	prunedDuringRetention, err := db.Prune(context.Background(), migratedAt.Add(-14*24*time.Hour))
	require.NoError(t, err)
	unseen, err := db.Unseen(context.Background(), []alib.Book{book})
	require.NoError(t, err)
	prunedAfterRetention, err := db.Prune(context.Background(), migratedAt.Add(time.Nanosecond))

	// Then
	require.NoError(t, err)
	require.Zero(t, prunedDuringRetention)
	require.Empty(t, unseen)
	require.Equal(t, 1, prunedAfterRetention)
}

func writeLegacyMarker(path, buyURL string) error {
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		return err
	}
	writeErr := db.Update(func(tx *bolt.Tx) error {
		bucket, bucketErr := tx.CreateBucketIfNotExists([]byte("sent_books"))
		if bucketErr != nil {
			return bucketErr
		}
		return bucket.Put([]byte(buyURL), []byte{1})
	})

	return errors.Join(writeErr, db.Close())
}
