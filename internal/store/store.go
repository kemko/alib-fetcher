// Package store persists discovered listing records.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kemko/alib-fetcher/internal/alib"

	bolt "go.etcd.io/bbolt"
)

var sentBucket = []byte("sent_books")

const timestampLayout = time.RFC3339Nano

type bookRecord struct {
	Book       alib.Book `json:"book"`
	ObservedAt int64     `json:"observed_at"`
	SentAt     int64     `json:"sent_at,omitempty"`
	Sent       bool      `json:"sent"`
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
	created := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(sentBucket)
		for _, book := range books {
			if err := ctx.Err(); err != nil {
				return err
			}

			key := []byte(book.BuyURL)
			value := bucket.Get(key)
			record := bookRecord{
				Book:       book,
				ObservedAt: encodeRecordTime(observedAt),
			}
			if value == nil {
				created++
			} else {
				existing, decodeErr := decodeRecord(key, value)
				if decodeErr != nil {
					return decodeErr
				}
				record.Sent = existing.Sent
				record.SentAt = existing.SentAt
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

// Pending returns discovered books not yet delivered to Telegram.
func (s *Store) Pending(ctx context.Context) ([]alib.Book, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("load pending books: %w", err)
	}

	pending := make([]alib.Book, 0)
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
				pending = append(pending, record.Book)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load pending books: %w", err)
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
			record := bookRecord{
				Book:       book,
				ObservedAt: encodeRecordTime(sentAt),
			}
			if value := bucket.Get(key); value != nil {
				existing, decodeErr := decodeRecord(key, value)
				if decodeErr != nil {
					return decodeErr
				}
				record = existing
				if record.ObservedAt == 0 {
					record.ObservedAt = encodeRecordTime(sentAt)
				}
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
		if _, err := decodeRecord(key, value); err == nil {
			return nil
		}

		sentAt := migratedAt.UTC()
		if parsedAt, err := time.Parse(timestampLayout, string(value)); err == nil {
			sentAt = parsedAt.UTC()
		}
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

func decodeRecord(key, value []byte) (bookRecord, error) {
	var record bookRecord
	if err := json.Unmarshal(value, &record); err != nil {
		return bookRecord{}, fmt.Errorf("decode book record %q: %w", string(key), err)
	}
	if record.Book.BuyURL == "" {
		return bookRecord{}, fmt.Errorf("decode book record %q: missing buy URL", string(key))
	}

	return record, nil
}

func encodeRecordTime(value time.Time) int64 {
	return value.UTC().UnixNano()
}

func decodeRecordTime(value int64) time.Time {
	return time.Unix(0, value).UTC()
}

func putRecord(bucket *bolt.Bucket, key []byte, record bookRecord) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode book record %q: %w", string(key), err)
	}
	return bucket.Put(key, encoded)
}
