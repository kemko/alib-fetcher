// Package store persists discovered listing records.
package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/kemko/alib-fetcher/internal/alib"

	bolt "go.etcd.io/bbolt"
)

var sentBucket = []byte("sent_books")

type bookRecord struct {
	Book       alib.Book `json:"book"`
	ObservedAt int64     `json:"observed_at"`
	SentAt     int64     `json:"sent_at,omitempty"`
	QueueOrder uint64    `json:"queue_order,omitempty"`
	Sent       bool      `json:"sent"`
}

type pendingRecord struct {
	book       alib.Book
	observedAt int64
	queueOrder uint64
}

type latestRecord struct {
	key        []byte
	observedAt int64
	queueOrder uint64
}

type legacyMigration struct {
	key    []byte
	record bookRecord
}

// Store persists discovered listings and their delivery state.
type Store struct {
	db *bolt.DB
}

// Open creates or opens the state database.
func Open(path string, migratedAt time.Time) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}

	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open state database: %w", err)
	}

	store := &Store{db: db}
	if initializationErr := db.Update(func(tx *bolt.Tx) error {
		bucket, createErr := tx.CreateBucketIfNotExists(sentBucket)
		if createErr != nil {
			return createErr
		}
		return migrateLegacyMarkers(bucket, migratedAt)
	}); initializationErr != nil {
		initializationErr = fmt.Errorf("initialize state database: %w", initializationErr)
		if closeErr := db.Close(); closeErr != nil {
			return nil, errors.Join(initializationErr, fmt.Errorf("close state database: %w", closeErr))
		}
		return nil, initializationErr
	}

	return store, nil
}

// RecordDiscovered stores fetched books and returns how many records were created.
func (s *Store) RecordDiscovered(ctx context.Context, books []alib.Book, observedAt time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("record discovered books: %w", err)
	}

	observedAt = observedAt.UTC()
	observedAtNanos := encodeRecordTime(observedAt)
	created := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(sentBucket)
		for _, book := range books {
			if err := ctx.Err(); err != nil {
				return err
			}

			key := []byte(book.BuyURL)
			record, isNew, prepareErr := prepareDiscoveredRecord(bucket, key, book, observedAtNanos)
			if prepareErr != nil {
				return prepareErr
			}
			if isNew {
				created++
			}
			if putErr := putRecord(bucket, key, record); putErr != nil {
				return putErr
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("record discovered books: %w", err)
	}

	return created, nil
}

func prepareDiscoveredRecord(
	bucket *bolt.Bucket,
	key []byte,
	book alib.Book,
	observedAt int64,
) (bookRecord, bool, error) {
	record := bookRecord{
		Book:       book,
		ObservedAt: observedAt,
	}
	value := bucket.Get(key)
	if value == nil {
		queueOrder, err := nextQueueOrder(bucket)
		record.QueueOrder = queueOrder

		return record, true, err
	}

	existing, err := decodeRecord(key, value)
	if err != nil {
		return bookRecord{}, false, err
	}
	record.Book = alib.MergePhotoResults(book, existing.Book)
	record.Sent = existing.Sent
	record.SentAt = existing.SentAt
	record.QueueOrder = existing.QueueOrder
	if record.QueueOrder != 0 || record.Sent {
		return record, false, nil
	}

	queueOrder, err := nextQueueOrder(bucket)
	record.QueueOrder = queueOrder

	return record, false, err
}

// SavePrepared updates a pending book without changing its delivery metadata.
func (s *Store) SavePrepared(ctx context.Context, book alib.Book) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("save prepared book: %w", err)
	}

	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}

		bucket := tx.Bucket(sentBucket)
		key := []byte(book.BuyURL)
		value := bucket.Get(key)
		if value == nil {
			return fmt.Errorf("missing book record %q", book.BuyURL)
		}
		record, decodeErr := decodeRecord(key, value)
		if decodeErr != nil {
			return decodeErr
		}
		if record.Sent {
			return fmt.Errorf("book record %q is already sent", book.BuyURL)
		}
		record.Book = book
		return putRecord(bucket, key, record)
	})
	if err != nil {
		return fmt.Errorf("save prepared book: %w", err)
	}

	return nil
}

func nextQueueOrder(bucket *bolt.Bucket) (uint64, error) {
	queueOrder, err := bucket.NextSequence()
	if err != nil {
		return 0, fmt.Errorf("allocate queue order: %w", err)
	}

	return queueOrder, nil
}

// Pending returns discovered books not yet delivered to Telegram.
func (s *Store) Pending(ctx context.Context) ([]alib.Book, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("load pending books: %w", err)
	}

	pendingRecords := make([]pendingRecord, 0)
	err := s.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(sentBucket).Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}

			record, decodeErr := decodeRecord(key, value)
			if decodeErr != nil {
				return decodeErr
			}
			if !record.Sent {
				pendingRecords = append(pendingRecords, pendingRecord{
					book:       record.Book,
					observedAt: record.ObservedAt,
					queueOrder: record.QueueOrder,
				})
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load pending books: %w", err)
	}

	sort.SliceStable(pendingRecords, func(leftIndex, rightIndex int) bool {
		left := pendingRecords[leftIndex]
		right := pendingRecords[rightIndex]
		if left.queueOrder > 0 && right.queueOrder > 0 && left.queueOrder != right.queueOrder {
			return left.queueOrder < right.queueOrder
		}
		if left.observedAt != right.observedAt {
			return left.observedAt < right.observedAt
		}

		return left.book.BuyURL < right.book.BuyURL
	})

	pending := make([]alib.Book, 0, len(pendingRecords))
	for _, record := range pendingRecords {
		pending = append(pending, record.book)
	}

	return pending, nil
}

// MarkSent records a successfully delivered group of books.
func (s *Store) MarkSent(ctx context.Context, books []alib.Book, sentAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("mark books sent: %w", err)
	}

	sentAt = sentAt.UTC()
	if err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(sentBucket)
		for _, book := range books {
			if err := ctx.Err(); err != nil {
				return err
			}

			key := []byte(book.BuyURL)
			value := bucket.Get(key)
			if value == nil {
				return fmt.Errorf("missing book record %q", book.BuyURL)
			}
			record, decodeErr := decodeRecord(key, value)
			if decodeErr != nil {
				return decodeErr
			}
			if record.ObservedAt == 0 {
				record.ObservedAt = encodeRecordTime(sentAt)
			}
			record.Sent = true
			record.SentAt = encodeRecordTime(sentAt)
			if putErr := putRecord(bucket, key, record); putErr != nil {
				return putErr
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("mark books sent: %w", err)
	}

	return nil
}

// Prune deletes identifiers sent strictly before the cutoff time.
func (s *Store) Prune(ctx context.Context, before time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("prune sent books: %w", err)
	}

	pruned := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(sentBucket).Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			record, decodeErr := decodeRecord(key, value)
			if decodeErr != nil {
				return decodeErr
			}
			if record.Sent && record.SentAt != 0 && decodeRecordTime(record.SentAt).Before(before) {
				if deleteErr := cursor.Delete(); deleteErr != nil {
					return deleteErr
				}
				pruned++
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("prune sent books: %w", err)
	}

	return pruned, nil
}

// DeleteLatest removes up to limit records in reverse discovery order.
func (s *Store) DeleteLatest(ctx context.Context, limit int) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("delete latest books: %w", err)
	}
	if limit <= 0 {
		return 0, errors.New("delete latest books: limit must be positive")
	}

	deleted := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}

		bucket := tx.Bucket(sentBucket)
		records, err := latestRecords(ctx, bucket)
		if err != nil {
			return err
		}

		sort.SliceStable(records, func(leftIndex, rightIndex int) bool {
			return latestRecordIsNewer(records[leftIndex], records[rightIndex])
		})

		deleted, err = deleteLatestRecords(ctx, bucket, records, limit)

		return err
	})
	if err != nil {
		return 0, fmt.Errorf("delete latest books: %w", err)
	}

	return deleted, nil
}

func latestRecords(ctx context.Context, bucket *bolt.Bucket) ([]latestRecord, error) {
	records := make([]latestRecord, 0)
	cursor := bucket.Cursor()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		record, err := decodeRecord(key, value)
		if err != nil {
			return nil, err
		}
		records = append(records, latestRecord{
			key:        append([]byte(nil), key...),
			observedAt: record.ObservedAt,
			queueOrder: record.QueueOrder,
		})
	}

	return records, nil
}

func latestRecordIsNewer(left, right latestRecord) bool {
	leftHasQueueOrder := left.queueOrder > 0
	rightHasQueueOrder := right.queueOrder > 0
	if leftHasQueueOrder != rightHasQueueOrder {
		return leftHasQueueOrder
	}
	if left.queueOrder != right.queueOrder {
		return left.queueOrder > right.queueOrder
	}
	if left.observedAt != right.observedAt {
		return left.observedAt > right.observedAt
	}

	return bytes.Compare(left.key, right.key) > 0
}

func deleteLatestRecords(
	ctx context.Context,
	bucket *bolt.Bucket,
	records []latestRecord,
	limit int,
) (int, error) {
	if limit > len(records) {
		limit = len(records)
	}
	deleted := 0
	for _, record := range records[:limit] {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if err := bucket.Delete(record.key); err != nil {
			return 0, err
		}
		deleted++
	}

	return deleted, nil
}

// Close closes the state database.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close state database: %w", err)
	}

	return nil
}

func migrateLegacyMarkers(bucket *bolt.Bucket, migratedAt time.Time) error {
	migrations := make([]legacyMigration, 0)
	if err := bucket.ForEach(func(key, value []byte) error {
		if isStructuredRecord(value) {
			_, err := decodeRecord(key, value)
			return err
		}

		sentAt := migratedAt.UTC()
		migrations = append(migrations, legacyMigration{
			key: append([]byte(nil), key...),
			record: bookRecord{
				Book:       alib.Book{BuyURL: string(key)},
				Sent:       true,
				ObservedAt: encodeRecordTime(sentAt),
				SentAt:     encodeRecordTime(sentAt),
			},
		})
		return nil
	}); err != nil {
		return err
	}
	for _, migration := range migrations {
		if err := putRecord(bucket, migration.key, migration.record); err != nil {
			return err
		}
	}

	return nil
}

func isStructuredRecord(value []byte) bool {
	trimmed := bytes.TrimSpace(value)

	return len(trimmed) > 0 && trimmed[0] == '{'
}

func decodeRecord(key, value []byte) (bookRecord, error) {
	var record bookRecord
	if err := json.Unmarshal(value, &record); err != nil {
		return bookRecord{}, fmt.Errorf("decode book record %q: %w", string(key), err)
	}
	if err := validateRecord(key, record); err != nil {
		return bookRecord{}, err
	}

	return record, nil
}

func validateRecord(key []byte, record bookRecord) error {
	if record.Book.BuyURL == "" {
		return fmt.Errorf("decode book record %q: missing buy URL", string(key))
	}
	if record.Book.BuyURL != string(key) {
		return fmt.Errorf("decode book record %q: buy URL does not match key", string(key))
	}

	return nil
}

func encodeRecordTime(value time.Time) int64 {
	return value.UTC().UnixNano()
}

func decodeRecordTime(value int64) time.Time {
	return time.Unix(0, value).UTC()
}

func putRecord(bucket *bolt.Bucket, key []byte, record bookRecord) error {
	if err := validateRecord(key, record); err != nil {
		return err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode book record %q: %w", string(key), err)
	}
	return bucket.Put(key, encoded)
}
