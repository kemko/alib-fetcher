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
	QueueOrder uint64    `json:"queue_order,omitempty"`
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
			"bibliography": "Full bibliography, 2026 г.",
			"content": "Full content",
			"seller": "Seller name",
			"seller_url": "https://example.com/seller",
			"location": "Moscow",
			"price": "100 rub.",
			"condition": "Condition: Full",
			"buy_url": "https://example.com/schema",
			"publication_year": 2026,
			"photo_urls": ["https://example.com/photo"]
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
	require.Equal(t, uint64(1), record.QueueOrder)
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

func Test_Store_delete_latest_removes_sent_and_pending_records_by_queue_order(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(path, time.Now())
	require.NoError(t, err)
	books := []alib.Book{
		{BuyURL: "https://example.com/z-first"},
		{BuyURL: "https://example.com/a-second"},
		{BuyURL: "https://example.com/m-third"},
	}
	recordDiscovered(t, db, books, time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC))
	require.NoError(t, db.MarkSent(context.Background(), []alib.Book{books[2]}, time.Now()))

	// When
	deleted, err := db.DeleteLatest(context.Background(), 2)
	require.NoError(t, db.Close())

	// Then
	require.NoError(t, err)
	require.Equal(t, 2, deleted)
	require.True(t, recordExists(t, path, books[0].BuyURL))
	require.False(t, recordExists(t, path, books[1].BuyURL))
	require.False(t, recordExists(t, path, books[2].BuyURL))
}

func Test_Store_delete_latest_deletes_all_records_when_limit_exceeds_database_size(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(path, time.Now())
	require.NoError(t, err)
	books := []alib.Book{{BuyURL: "https://example.com/first"}, {BuyURL: "https://example.com/second"}}
	recordDiscovered(t, db, books, time.Now())

	// When
	deleted, err := db.DeleteLatest(context.Background(), 3)
	require.NoError(t, db.Close())

	// Then
	require.NoError(t, err)
	require.Equal(t, 2, deleted)
	for _, book := range books {
		require.False(t, recordExists(t, path, book.BuyURL))
	}
}

func Test_Store_delete_latest_rejects_non_positive_limit_without_deleting(t *testing.T) {
	t.Parallel()

	for _, limit := range []int{0, -1} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			// Given
			path := filepath.Join(t.TempDir(), "state.db")
			db, err := store.Open(path, time.Now())
			require.NoError(t, err)
			book := alib.Book{BuyURL: "https://example.com/book"}
			recordDiscovered(t, db, []alib.Book{book}, time.Now())

			// When
			deleted, err := db.DeleteLatest(context.Background(), limit)
			require.NoError(t, db.Close())

			// Then
			require.ErrorContains(t, err, "limit must be positive")
			require.Zero(t, deleted)
			require.True(t, recordExists(t, path, book.BuyURL))
		})
	}
}

func Test_Store_delete_latest_rolls_back_when_context_is_canceled(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(path, time.Now())
	require.NoError(t, err)
	books := []alib.Book{{BuyURL: "https://example.com/first"}, {BuyURL: "https://example.com/second"}}
	recordDiscovered(t, db, books, time.Now())
	ctx := &cancelAfterErrContext{Context: context.Background(), cancelAt: 6}

	// When
	deleted, err := db.DeleteLatest(ctx, 2)
	require.NoError(t, db.Close())

	// Then
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, deleted)
	for _, book := range books {
		require.True(t, recordExists(t, path, book.BuyURL))
	}
}

func Test_Store_delete_latest_uses_observed_at_and_key_for_equal_queue_order(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	olderURL := "https://example.com/older"
	newerURL := "https://example.com/a-newer"
	newerTieURL := "https://example.com/z-newer"
	olderObservedAt := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC).UnixNano()
	records := []storedRecord{
		{Book: alib.Book{BuyURL: olderURL}, ObservedAt: olderObservedAt, QueueOrder: 7},
		{Book: alib.Book{BuyURL: newerURL}, ObservedAt: olderObservedAt + 1, QueueOrder: 7},
		{Book: alib.Book{BuyURL: newerTieURL}, ObservedAt: olderObservedAt + 1, QueueOrder: 7},
	}
	require.NoError(t, writeStoredRecords(path, records))
	db, err := store.Open(path, time.Now())
	require.NoError(t, err)

	// When
	deleted, err := db.DeleteLatest(context.Background(), 2)
	require.NoError(t, db.Close())

	// Then
	require.NoError(t, err)
	require.Equal(t, 2, deleted)
	require.True(t, recordExists(t, path, olderURL))
	require.False(t, recordExists(t, path, newerURL))
	require.False(t, recordExists(t, path, newerTieURL))
}

func Test_Store_delete_latest_orders_queue_ordered_records_before_missing_records(t *testing.T) {
	t.Parallel()

	for _, urls := range [][]string{
		{"https://example.com/a", "https://example.com/m", "https://example.com/z"},
		{"https://example.com/m", "https://example.com/z", "https://example.com/a"},
		{"https://example.com/z", "https://example.com/a", "https://example.com/m"},
	} {
		// Given
		path := filepath.Join(t.TempDir(), "state.db")
		observedAt := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC).UnixNano()
		records := []storedRecord{
			{Book: alib.Book{BuyURL: urls[0]}, ObservedAt: observedAt + 2, QueueOrder: 1},
			{Book: alib.Book{BuyURL: urls[1]}, ObservedAt: observedAt + 1},
			{Book: alib.Book{BuyURL: urls[2]}, ObservedAt: observedAt, QueueOrder: 2},
		}
		require.NoError(t, writeStoredRecords(path, records))
		db, err := store.Open(path, time.Now())
		require.NoError(t, err)

		// When
		deleted, err := db.DeleteLatest(context.Background(), 2)
		require.NoError(t, db.Close())

		// Then
		require.NoError(t, err)
		require.Equal(t, 2, deleted)
		require.False(t, recordExists(t, path, urls[0]))
		require.True(t, recordExists(t, path, urls[1]))
		require.False(t, recordExists(t, path, urls[2]))
	}
}

func Test_Store_delete_latest_rediscovery_requeues_book_with_next_sequence(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(path, time.Now())
	require.NoError(t, err)
	remainingBook := alib.Book{BuyURL: "https://example.com/remaining"}
	forgottenBook := alib.Book{BuyURL: "https://example.com/forgotten"}
	observedAt := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	recordDiscovered(t, db, []alib.Book{remainingBook, forgottenBook}, observedAt)
	require.NoError(t, db.MarkSent(context.Background(), []alib.Book{forgottenBook}, observedAt))
	require.Equal(t, 1, mustDeleteLatest(t, db, 1))

	// When
	created, err := db.RecordDiscovered(context.Background(), []alib.Book{forgottenBook}, observedAt.Add(time.Hour))
	pending, pendingErr := db.Pending(context.Background())
	require.NoError(t, db.Close())

	// Then
	require.NoError(t, err)
	require.NoError(t, pendingErr)
	require.Equal(t, 1, created)
	require.Equal(t, []alib.Book{remainingBook, forgottenBook}, pending)
	forgottenRecord := readStoredRecord(t, path, forgottenBook.BuyURL)
	require.Equal(t, uint64(3), forgottenRecord.QueueOrder)
	require.False(t, forgottenRecord.Sent)
	require.Zero(t, forgottenRecord.SentAt)
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
		"queue_order": 19,
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

func Test_Store_loads_legacy_pending_record_and_rewrites_it_only_on_mutating_write(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	buyURL := "https://example.com/legacy-pending"
	observedAt := time.Date(2026, time.August, 4, 12, 0, 0, 123, time.UTC)
	sentAt := time.Date(2026, time.August, 5, 13, 0, 0, 456, time.UTC)
	legacyRecord := fmt.Sprintf(`{
		"book": {
			"title": "Legacy title",
			"text_before_seller": "Legacy bibliography, 2025 г.\n(Условия продажи продавца",
			"seller": "BS - Legacy seller",
			"seller_url": "https://example.com/legacy-seller",
			"text_before_buy": ", Moscow.) Цена: 250 rub.",
			"buy_url": %q,
			"text_after_buy": "\nLegacy content\nСостояние: Legacy condition",
			"has_photos": true
		},
		"observed_at": %d,
		"queue_order": 27,
		"sent": false
	}`, buyURL, observedAt.UnixNano())
	require.NoError(t, writeLegacyMarker(path, buyURL, []byte(legacyRecord)))
	db, err := store.Open(path, time.Now())
	require.NoError(t, err)

	// When
	pending, pendingErr := db.Pending(context.Background())
	require.NoError(t, db.Close())
	rawAfterOpen := readRawRecord(t, path, buyURL)
	db, err = store.Open(path, time.Now())
	require.NoError(t, err)
	markErr := db.MarkSent(context.Background(), []alib.Book{{BuyURL: buyURL}}, sentAt)
	require.NoError(t, db.Close())
	rewritten := readStoredRecord(t, path, buyURL)
	rewrittenRaw := readRawRecord(t, path, buyURL)

	// Then
	require.NoError(t, pendingErr)
	require.NoError(t, markErr)
	expectedPending := alib.Book{
		Title:           "Legacy title",
		Bibliography:    "Legacy bibliography, 2025 г.",
		PublicationYear: 2025,
		Content:         "Legacy content",
		Seller:          "Legacy seller",
		SellerURL:       "https://example.com/legacy-seller",
		Location:        "Moscow",
		Price:           "250 rub.",
		Condition:       "Состояние: Legacy condition",
		BuyURL:          buyURL,
		PhotoURLs:       nil,
	}
	require.Equal(t, []alib.Book{expectedPending}, pending)
	require.Equal(t, []byte(legacyRecord), rawAfterOpen)
	require.Equal(t, expectedPending, rewritten.Book)
	require.Equal(t, observedAt.UnixNano(), rewritten.ObservedAt)
	require.Equal(t, uint64(27), rewritten.QueueOrder)
	require.True(t, rewritten.Sent)
	require.Equal(t, sentAt.UnixNano(), rewritten.SentAt)
	require.NotContains(t, string(rewrittenRaw), "text_before_seller")
	require.NotContains(t, string(rewrittenRaw), "text_before_buy")
	require.NotContains(t, string(rewrittenRaw), "text_after_buy")
}

func Test_Open_rejects_malformed_json_record_without_replacing_it(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	buyURL := "https://example.com/malformed-record"
	malformedRecord := []byte(" \n\t" + `{"book":`)
	require.NoError(t, writeLegacyMarker(path, buyURL, malformedRecord))

	// When
	db, err := store.Open(path, time.Now())

	// Then
	require.Nil(t, db)
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode book record")
	require.Equal(t, malformedRecord, readRawRecord(t, path, buyURL))
}

func Test_Open_rejects_json_record_with_buy_url_different_from_key(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	buyURL := "https://example.com/record-key"
	record := []byte(`{"book":{"buy_url":"https://example.com/record-value"},"sent":true}`)
	require.NoError(t, writeLegacyMarker(path, buyURL, record))

	// When
	db, err := store.Open(path, time.Now())

	// Then
	require.Nil(t, db)
	require.Error(t, err)
	require.Contains(t, err.Error(), "buy URL does not match key")
	require.Equal(t, record, readRawRecord(t, path, buyURL))
}

func Test_Open_rolls_back_neighboring_legacy_migration_when_record_has_no_buy_url(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	legacyURL := "https://example.com/a-legacy"
	corruptURL := "https://example.com/z-corrupt"
	legacyMarker := []byte{1}
	corruptRecord := []byte(`{"book":{},"sent":true}`)
	require.NoError(t, writeLegacyMarker(path, legacyURL, legacyMarker))
	require.NoError(t, writeLegacyMarker(path, corruptURL, corruptRecord))

	// When
	db, err := store.Open(path, time.Now())

	// Then
	require.Nil(t, db)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing buy URL")
	require.Equal(t, legacyMarker, readRawRecord(t, path, legacyURL))
	require.Equal(t, corruptRecord, readRawRecord(t, path, corruptURL))
}

func Test_Open_migrates_legacy_timestamp_marker_to_sent_record(t *testing.T) {
	t.Parallel()

	// Given
	path := filepath.Join(t.TempDir(), "state.db")
	buyURL := "https://example.com/legacy-timestamp"
	legacySentAt := time.Date(2026, time.July, 1, 12, 0, 0, 123, time.UTC)
	migratedAt := time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)
	require.NoError(t, writeLegacyMarker(path, buyURL, []byte(legacySentAt.Format(time.RFC3339Nano))))

	// When
	db, err := store.Open(path, migratedAt)
	require.NoError(t, err)
	pruned, pruneErr := db.Prune(context.Background(), migratedAt.Add(-14*24*time.Hour))
	pending, pendingErr := db.Pending(context.Background())
	require.NoError(t, db.Close())
	record := readStoredRecord(t, path, buyURL)

	// Then
	require.NoError(t, pruneErr)
	require.NoError(t, pendingErr)
	require.Zero(t, pruned)
	require.Empty(t, pending)
	require.Equal(t, alib.Book{BuyURL: buyURL}, record.Book)
	require.Empty(t, record.Book.Title)
	require.Empty(t, record.Book.Seller)
	require.Empty(t, record.Book.PhotoURLs)
	require.True(t, record.Sent)
	require.True(t, decodeStoredTime(record.SentAt).Equal(migratedAt.UTC()))
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
	require.Empty(t, record.Book.PhotoURLs)
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
	migratedAt := time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)
	rediscoveredAt := sentAt.Add(24 * time.Hour)
	require.NoError(t, writeLegacyMarker(path, buyURL, []byte(sentAt.Format(time.RFC3339Nano))))
	db, err := store.Open(path, migratedAt)
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
	require.True(t, decodeStoredTime(record.SentAt).Equal(migratedAt.UTC()))
	require.True(t, decodeStoredTime(record.ObservedAt).Equal(rediscoveredAt.UTC()))
}

func fullBook(buyURL string) alib.Book {
	return alib.Book{
		Title:           "Full title",
		Bibliography:    "Full bibliography, 2026 г.",
		PublicationYear: 2026,
		Content:         "Full content",
		Seller:          "Seller name",
		SellerURL:       "https://example.com/seller",
		Location:        "Moscow",
		Price:           "100 rub.",
		Condition:       "Condition: Full",
		BuyURL:          buyURL,
		PhotoURLs:       []string{"https://example.com/photo"},
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

func mustDeleteLatest(t *testing.T, db *store.Store, limit int) int {
	t.Helper()

	deleted, err := db.DeleteLatest(context.Background(), limit)
	require.NoError(t, err)

	return deleted
}

type cancelAfterErrContext struct {
	context.Context
	cancelAt int
	errCalls int
}

func (c *cancelAfterErrContext) Err() error {
	c.errCalls++
	if c.errCalls >= c.cancelAt {
		return context.Canceled
	}

	return c.Context.Err()
}

func writeStoredRecords(path string, records []storedRecord) error {
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		return err
	}
	writeErr := db.Update(func(tx *bolt.Tx) error {
		bucket, bucketErr := tx.CreateBucketIfNotExists([]byte("sent_books"))
		if bucketErr != nil {
			return bucketErr
		}
		for _, record := range records {
			value, marshalErr := json.Marshal(record)
			if marshalErr != nil {
				return marshalErr
			}
			if putErr := bucket.Put([]byte(record.Book.BuyURL), value); putErr != nil {
				return putErr
			}
		}
		return nil
	})

	return errors.Join(writeErr, db.Close())
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
