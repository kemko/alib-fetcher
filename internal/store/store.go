// Package store persists delivered listing identifiers.
package store

import (
	"bytes"
	"context"
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

// Store persists links for listings already delivered to Telegram.
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

// Unseen returns books whose buy links are absent from the state database.
func (s *Store) Unseen(ctx context.Context, books []alib.Book) ([]alib.Book, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("filter unseen books: %w", err)
	}

	unseen := make([]alib.Book, 0, len(books))
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(sentBucket)
		for _, book := range books {
			if bucket.Get([]byte(book.BuyURL)) == nil {
				unseen = append(unseen, book)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("filter unseen books: %w", err)
	}

	return unseen, nil
}

// MarkSent stores the buy links for a successfully delivered group of books.
func (s *Store) MarkSent(ctx context.Context, books []alib.Book, sentAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("mark books sent: %w", err)
	}

	if err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(sentBucket)
		for _, book := range books {
			if err := bucket.Put([]byte(book.BuyURL), encodeTimestamp(sentAt)); err != nil {
				return err
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
			sentAt, err := time.Parse(timestampLayout, string(value))
			if err != nil {
				return fmt.Errorf("parse sent timestamp: %w", err)
			}
			if sentAt.Before(before) {
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
	legacyKeys := make([][]byte, 0)
	if err := bucket.ForEach(func(key, value []byte) error {
		if _, err := time.Parse(timestampLayout, string(value)); err != nil {
			legacyKeys = append(legacyKeys, bytes.Clone(key))
		}
		return nil
	}); err != nil {
		return err
	}
	for _, key := range legacyKeys {
		if err := bucket.Put(key, encodeTimestamp(migratedAt)); err != nil {
			return err
		}
	}

	return nil
}

func encodeTimestamp(value time.Time) []byte {
	return []byte(value.UTC().Format(timestampLayout))
}
