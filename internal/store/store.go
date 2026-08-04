// Package store persists delivered listing identifiers.
package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kemmko/alib-fetcher/internal/alib"

	bolt "go.etcd.io/bbolt"
)

var sentBucket = []byte("sent_books")

// Store persists links for listings already delivered to Telegram.
type Store struct {
	db *bolt.DB
}

// Open creates or opens the state database.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}

	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open state database: %w", err)
	}

	store := &Store{db: db}
	if initializationErr := db.Update(func(tx *bolt.Tx) error {
		_, createErr := tx.CreateBucketIfNotExists(sentBucket)
		return createErr
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
func (s *Store) MarkSent(ctx context.Context, books []alib.Book) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("mark books sent: %w", err)
	}

	if err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(sentBucket)
		for _, book := range books {
			if err := bucket.Put([]byte(book.BuyURL), []byte{1}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("mark books sent: %w", err)
	}

	return nil
}

// Close closes the state database.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close state database: %w", err)
	}

	return nil
}
