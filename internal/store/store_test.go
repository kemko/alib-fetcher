package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/store"

	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

type storedRecord struct {
	Book       alib.Book `json:"book"`
	ObservedAt int64     `json:"observed_at"`
	SentAt     int64     `json:"sent_at,omitempty"`
	Sent       bool      `json:"sent"`
}

func Test_Store_records_new_books_as_pending_with_full_payload(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	observedAt := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	db, err := store.Open(path, time.Now())
	require.NoError(t, err)
	book := fullBook("https://example.com/recorded")

	// When
	created, err := db.RecordDiscovered(context.Background(), []alib.Book{book}, observedAt)
	pending, pendingErr := db.Pending(context.Background())
	require.NoError(t, db.Close())
	record := readStoredRecord(t, path, book.BuyURL)

	// Then
	require.NoError(t, err)
	require.NoError(t, pendingErr)
	require.Equal(t, 1, created)
	require.Equal(t, []alib.Book{book}, pending)
	require.Equal(t, book, record.Book)
	require.False(t, record.Sent)
	require.True(t, decodeStoredTime(record.ObservedAt).Equal(observedAt.UTC()))
	require.Zero(t, record.SentAt)
}

func Test_Store_persists_stable_json_schema(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	observedAt := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	db, err := store.Open(path, time.Now())
	require.NoError(t, err)
	book := fullBook("https://example.com/schema")

	// When
	created, err := db.RecordDiscovered(context.Background(), []alib.Book{book}, observedAt)
	require.NoError(t, db.Close())
	rawRecord := readRawRecord(t, path, book.BuyURL)

	// Then
	require.NoError(t, err)
	require.Equal(t, 1, created)
	require.JSONEq(t, fmt.Sprintf(`{
		"book": {
			"title": "Full title",
			"text_before_seller": "Before seller",
			"seller": "Seller name",
			"seller_url": "https://example.com/seller",
			"text_before_buy": "Before buy",
			"buy_url": "https://example.com/schema",
			"text_after_buy": "After buy",
			"has_photos": true
			},
			"observed_at": %d,
			"queue_order": 1,
			"sent": false
		}`, observedAt.UnixNano()), string(rawRecord))
}

func Test_Store_updates_sent_book_metadata_without_requeueing(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	observedAt := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	sentAt := observedAt.Add(time.Hour)
	rediscoveredAt := observedAt.Add(2 * time.Hour)
	db, err := store.Open(path, time.Now())
	require.NoError(t, err)
	original := fullBook("https://example.com/sent")
	updated := fullBook("https://example.com/sent")
	updated.Title = "Updated title"
	updated.Seller = "Updated seller"
	recordDiscovered(t, db, []alib.Book{original}, observedAt)
	require.NoError(t, db.MarkSent(context.Background(), []alib.Book{original}, sentAt))

	// When
	created, err := db.RecordDiscovered(context.Background(), []alib.Book{updated}, rediscoveredAt)
	pending, pendingErr := db.Pending(context.Background())
	require.NoError(t, db.Close())
	record := readStoredRecord(t, path, updated.BuyURL)

	// Then
	require.NoError(t, err)
	require.NoError(t, pendingErr)
	require.Zero(t, created)
	require.Empty(t, pending)
	require.Equal(t, updated, record.Book)
	require.True(t, record.Sent)
	require.NotZero(t, record.SentAt)
	require.True(t, decodeStoredTime(record.SentAt).Equal(sentAt.UTC()))
	require.True(t, decodeStoredTime(record.ObservedAt).Equal(rediscoveredAt.UTC()))
}

func Test_Store_returns_pending_books_from_previous_failed_cycle(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(path, time.Now())
	require.NoError(t, err)
	previous := fullBook("https://example.com/1-previous")
	current := fullBook("https://example.com/2-current")
	recordDiscovered(t, db, []alib.Book{previous}, time.Now())

	// When
	recordDiscovered(t, db, []alib.Book{current}, time.Now())
	pending, err := db.Pending(context.Background())

	// Then
	require.NoError(t, err)
	require.NoError(t, db.Close())
	require.Equal(t, []alib.Book{previous, current}, pending)
}

func Test_Store_returns_pending_books_in_discovery_order(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	observedAt := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	db, err := store.Open(path, time.Now())
	require.NoError(t, err)
	books := []alib.Book{
		fullBook("https://example.com/z-last-key"),
		fullBook("https://example.com/a-first-key"),
		fullBook("https://example.com/m-middle-key"),
	}
	recordDiscovered(t, db, books, observedAt)

	// When
	pending, err := db.Pending(context.Background())
	require.NoError(t, db.Close())

	// Then
	require.NoError(t, err)
	require.Equal(t, books, pending)
}

func Test_Store_mark_sent_removes_books_from_pending_and_preserves_metadata(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	observedAt := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	sentAt := observedAt.Add(time.Hour)
	db, err := store.Open(path, time.Now())
	require.NoError(t, err)
	book := fullBook("https://example.com/delivered")
	recordDiscovered(t, db, []alib.Book{book}, observedAt)

	// When
	require.NoError(t, db.MarkSent(context.Background(), []alib.Book{{BuyURL: book.BuyURL}}, sentAt))
	pending, err := db.Pending(context.Background())
	require.NoError(t, db.Close())
	record := readStoredRecord(t, path, book.BuyURL)

	// Then
	require.NoError(t, err)
	require.Empty(t, pending)
	require.Equal(t, book, record.Book)
	require.True(t, record.Sent)
	require.NotZero(t, record.SentAt)
	require.True(t, decodeStoredTime(record.SentAt).Equal(sentAt.UTC()))
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
	path := filepath.Join(t.TempDir(), "state.db")
	now := time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)
	db, err := store.Open(path, now)
	require.NoError(t, err)
	oldBook := alib.Book{BuyURL: "https://example.com/old"}
	boundaryBook := alib.Book{BuyURL: "https://example.com/boundary"}
	recordDiscovered(t, db, []alib.Book{oldBook, boundaryBook}, now)
	require.NoError(t, db.MarkSent(context.Background(), []alib.Book{oldBook}, now.Add(-15*24*time.Hour)))
	require.NoError(t, db.MarkSent(context.Background(), []alib.Book{boundaryBook}, now.Add(-14*24*time.Hour)))

	// When
	pruned, err := db.Prune(context.Background(), now.Add(-14*24*time.Hour))
	require.NoError(t, db.Close())

	// Then
	require.NoError(t, err)
	require.Equal(t, 1, pruned)
	require.False(t, recordExists(t, path, oldBook.BuyURL))
	require.True(t, recordExists(t, path, boundaryBook.BuyURL))
}

func Test_Store_mark_sent_rejects_missing_record(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(path, time.Now())
	require.NoError(t, err)
	book := alib.Book{BuyURL: "https://example.com/missing"}

	// When
	err = db.MarkSent(context.Background(), []alib.Book{book}, time.Now())
	require.NoError(t, db.Close())

	// Then
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing book record")
	require.False(t, recordExists(t, path, book.BuyURL))
}

func Test_Store_prune_keeps_unsent_entries_older_than_cutoff(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	now := time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)
	db, err := store.Open(path, now)
	require.NoError(t, err)
	pendingBook := fullBook("https://example.com/pending-old")
	recordDiscovered(t, db, []alib.Book{pendingBook}, now.Add(-30*24*time.Hour))

	// When
	pruned, err := db.Prune(context.Background(), now.Add(-14*24*time.Hour))
	pending, pendingErr := db.Pending(context.Background())
	require.NoError(t, db.Close())

	// Then
	require.NoError(t, err)
	require.NoError(t, pendingErr)
	require.Zero(t, pruned)
	require.Equal(t, []alib.Book{pendingBook}, pending)
	require.True(t, recordExists(t, path, pendingBook.BuyURL))
}

func Test_Open_leaves_valid_json_records_unchanged(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	buyURL := "https://example.com/json-record"
	observedAt := time.Date(2026, time.August, 4, 12, 0, 0, 0, time.UTC).UnixNano()
	sentAt := time.Date(2026, time.August, 4, 13, 0, 0, 0, time.UTC).UnixNano()
	record := fmt.Sprintf(`{
		"book": {
			"title": "Stored title",
			"text_before_seller": "Stored before seller",
			"seller": "Stored seller",
			"seller_url": "https://example.com/seller",
			"text_before_buy": "Stored before buy",
			"buy_url": "https://example.com/json-record",
			"text_after_buy": "Stored after buy",
			"has_photos": true
		},
		"observed_at": %d,
		"sent_at": %d,
		"sent": true
	}`, observedAt, sentAt)
	require.NoError(t, writeLegacyMarker(path, buyURL, []byte(record)))

	// When
	db, err := store.Open(path, time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	// Then
	require.JSONEq(t, record, string(readRawRecord(t, path, buyURL)))
}

func Test_Open_migrates_legacy_timestamp_marker_to_sent_record(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	buyURL := "https://example.com/legacy-timestamp"
	sentAt := time.Date(2026, time.August, 4, 12, 0, 0, 123, time.UTC)
	require.NoError(t, writeLegacyMarker(path, buyURL, []byte(sentAt.Format(time.RFC3339Nano))))

	// When
	db, err := store.Open(path, time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	pending, pendingErr := db.Pending(context.Background())
	require.NoError(t, db.Close())
	record := readStoredRecord(t, path, buyURL)

	// Then
	require.NoError(t, pendingErr)
	require.Empty(t, pending)
	require.Equal(t, alib.Book{BuyURL: buyURL}, record.Book)
	require.Empty(t, record.Book.Title)
	require.Empty(t, record.Book.Seller)
	require.Empty(t, record.Book.TextBeforeSeller)
	require.Empty(t, record.Book.TextBeforeBuy)
	require.Empty(t, record.Book.TextAfterBuy)
	require.False(t, record.Book.HasPhotos)
	require.True(t, record.Sent)
	require.True(t, decodeStoredTime(record.SentAt).Equal(sentAt))
}

func Test_Open_migrates_unknown_legacy_marker_without_immediate_pruning(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	book := alib.Book{BuyURL: "https://example.com/legacy"}
	require.NoError(t, writeLegacyMarker(path, book.BuyURL, []byte{1}))
	migratedAt := time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)

	// When
	db, err := store.Open(path, migratedAt)
	require.NoError(t, err)
	prunedDuringRetention, err := db.Prune(context.Background(), migratedAt.Add(-14*24*time.Hour))
	require.NoError(t, err)
	pending, err := db.Pending(context.Background())
	require.NoError(t, err)
	require.NoError(t, db.Close())
	record := readStoredRecord(t, path, book.BuyURL)
	db, err = store.Open(path, migratedAt)
	require.NoError(t, err)
	prunedAfterRetention, err := db.Prune(context.Background(), migratedAt.Add(time.Nanosecond))
	require.NoError(t, db.Close())

	// Then
	require.NoError(t, err)
	require.Zero(t, prunedDuringRetention)
	require.Empty(t, pending)
	require.Equal(t, 1, prunedAfterRetention)
	require.Equal(t, book, record.Book)
	require.Empty(t, record.Book.Title)
	require.Empty(t, record.Book.Seller)
	require.Empty(t, record.Book.TextBeforeSeller)
	require.Empty(t, record.Book.TextBeforeBuy)
	require.Empty(t, record.Book.TextAfterBuy)
	require.False(t, record.Book.HasPhotos)
	require.True(t, record.Sent)
	require.NotZero(t, record.SentAt)
	require.True(t, decodeStoredTime(record.SentAt).Equal(migratedAt.UTC()))
}

func Test_Store_rediscovered_legacy_sent_book_gets_full_payload_without_requeueing(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	buyURL := "https://example.com/legacy-rediscovered"
	sentAt := time.Date(2026, time.August, 4, 12, 0, 0, 0, time.UTC)
	rediscoveredAt := sentAt.Add(24 * time.Hour)
	require.NoError(t, writeLegacyMarker(path, buyURL, []byte(sentAt.Format(time.RFC3339Nano))))
	db, err := store.Open(path, time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	rediscovered := fullBook(buyURL)

	// When
	created, err := db.RecordDiscovered(context.Background(), []alib.Book{rediscovered}, rediscoveredAt)
	pending, pendingErr := db.Pending(context.Background())
	require.NoError(t, db.Close())
	record := readStoredRecord(t, path, buyURL)

	// Then
	require.NoError(t, err)
	require.NoError(t, pendingErr)
	require.Zero(t, created)
	require.Empty(t, pending)
	require.Equal(t, rediscovered, record.Book)
	require.True(t, record.Sent)
	require.True(t, decodeStoredTime(record.SentAt).Equal(sentAt))
	require.True(t, decodeStoredTime(record.ObservedAt).Equal(rediscoveredAt.UTC()))
}

func fullBook(buyURL string) alib.Book {
	return alib.Book{
		Title:            "Full title",
		TextBeforeSeller: "Before seller",
		Seller:           "Seller name",
		SellerURL:        "https://example.com/seller",
		TextBeforeBuy:    "Before buy",
		BuyURL:           buyURL,
		TextAfterBuy:     "After buy",
		HasPhotos:        true,
	}
}

func recordDiscovered(t *testing.T, db *store.Store, books []alib.Book, observedAt time.Time) {
	t.Helper()

	_, err := db.RecordDiscovered(context.Background(), books, observedAt)
	require.NoError(t, err)
}

func decodeStoredTime(value int64) time.Time {
	return time.Unix(0, value).UTC()
}

func readStoredRecord(t *testing.T, path, buyURL string) storedRecord {
	t.Helper()

	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()

	var record storedRecord
	err = db.View(func(tx *bolt.Tx) error {
		value := tx.Bucket([]byte("sent_books")).Get([]byte(buyURL))
		if value == nil {
			return fmt.Errorf("read stored record %q: missing", buyURL)
		}
		return json.Unmarshal(value, &record)
	})
	require.NoError(t, err)

	return record
}

func readRawRecord(t *testing.T, path, buyURL string) []byte {
	t.Helper()

	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()

	var valueCopy []byte
	err = db.View(func(tx *bolt.Tx) error {
		value := tx.Bucket([]byte("sent_books")).Get([]byte(buyURL))
		if value == nil {
			return fmt.Errorf("read raw record %q: missing", buyURL)
		}
		valueCopy = append([]byte(nil), value...)
		return nil
	})
	require.NoError(t, err)

	return valueCopy
}

func recordExists(t *testing.T, path, buyURL string) bool {
	t.Helper()

	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()

	exists := false
	err = db.View(func(tx *bolt.Tx) error {
		exists = tx.Bucket([]byte("sent_books")).Get([]byte(buyURL)) != nil
		return nil
	})
	require.NoError(t, err)

	return exists
}

func writeLegacyMarker(path, buyURL string, value []byte) error {
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		return err
	}
	writeErr := db.Update(func(tx *bolt.Tx) error {
		bucket, bucketErr := tx.CreateBucketIfNotExists([]byte("sent_books"))
		if bucketErr != nil {
			return bucketErr
		}
		return bucket.Put([]byte(buyURL), value)
	})

	return errors.Join(writeErr, db.Close())
}
