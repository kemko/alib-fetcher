// Package app coordinates the daily digest use case.
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/digest"
)

// Fetcher obtains the latest source listings.
type Fetcher interface {
	Fetch(context.Context) ([]alib.Book, error)
}

// State tracks discovered listings and their delivery status.
type State interface {
	Prune(context.Context, time.Time) (int, error)
	RecordDiscovered(context.Context, []alib.Book, time.Time) (int, error)
	Pending(context.Context) ([]alib.Book, error)
	MarkSent(context.Context, []alib.Book, time.Time) error
}

// Sender delivers one rendered message.
type Sender interface {
	Send(ctx context.Context, text string, silent bool, attachRefresh bool) error
}

// Dependencies contains the service adapters, retry wait function, and Telegram message limit.
type Dependencies struct {
	Fetcher      Fetcher
	State        State
	Sender       Sender
	Now          func() time.Time
	Wait         func(context.Context, time.Duration) error
	MessageLimit int
}

// Result summarizes one completed or partially completed fetch job.
type Result struct {
	Fetched int
	New     int
	Sent    int
	Pruned  int
}

// Service coordinates fetching, deduplication, delivery, and acknowledgement.
type Service struct {
	dependencies Dependencies
}

// NewService builds the daily digest service.
func NewService(dependencies Dependencies) *Service {
	if dependencies.Wait == nil {
		dependencies.Wait = wait
	}
	return &Service{dependencies: dependencies}
}

// Run executes one complete digest cycle.
func (s *Service) Run(ctx context.Context) (Result, error) {
	cycleTime := s.dependencies.Now()
	pruned, err := s.dependencies.State.Prune(ctx, cycleTime.Add(-14*24*time.Hour))
	if err != nil {
		return Result{}, fmt.Errorf("prune delivered listings: %w", err)
	}
	result := Result{Pruned: pruned}

	books, err := s.dependencies.Fetcher.Fetch(ctx)
	if err != nil {
		return result, fmt.Errorf("fetch listings: %w", err)
	}
	result.Fetched = len(books)

	created, err := s.dependencies.State.RecordDiscovered(ctx, books, cycleTime)
	if err != nil {
		return result, fmt.Errorf("record discovered listings: %w", err)
	}
	result.New = created

	pending, err := s.dependencies.State.Pending(ctx)
	if err != nil {
		return result, fmt.Errorf("load pending listings: %w", err)
	}
	if len(pending) == 0 {
		return result, nil
	}

	chunks, skippedBuyURLs, err := renderSendable(pending, s.dependencies.MessageLimit)
	if err != nil {
		return result, fmt.Errorf("render digest: %w", err)
	}
	ackCtx := context.WithoutCancel(ctx)
	for index, chunk := range chunks {
		silent := index < len(chunks)-1
		attachRefresh := index == len(chunks)-1
		if sendErr := s.send(ctx, chunk.Text, silent, attachRefresh); sendErr != nil {
			return result, fmt.Errorf("send digest: %w", sendErr)
		}
		if markErr := s.dependencies.State.MarkSent(ackCtx, chunk.Books, cycleTime); markErr != nil {
			return result, fmt.Errorf("record delivered listings: %w", markErr)
		}
		result.Sent += len(chunk.Books)
	}
	if len(skippedBuyURLs) > 0 {
		return result, fmt.Errorf("render digest: %w: %s", digest.ErrMessageTooLong, strings.Join(skippedBuyURLs, ", "))
	}

	return result, nil
}

func renderSendable(books []alib.Book, limit int) ([]digest.Chunk, []string, error) {
	renderable := make([]alib.Book, 0, len(books))
	skippedBuyURLs := make([]string, 0)
	for _, book := range books {
		if _, err := digest.Render([]alib.Book{book}, limit); err != nil {
			if errors.Is(err, digest.ErrMessageTooLong) {
				skippedBuyURLs = append(skippedBuyURLs, book.BuyURL)
				continue
			}
			return nil, nil, err
		}
		renderable = append(renderable, book)
	}

	chunks, err := digest.Render(renderable, limit)
	if err != nil {
		return nil, nil, err
	}

	return chunks, skippedBuyURLs, nil
}

func (s *Service) send(ctx context.Context, text string, silent bool, attachRefresh bool) error {
	for {
		err := s.dependencies.Sender.Send(ctx, text, silent, attachRefresh)
		if err == nil {
			return nil
		}

		delay, retry := retryDelay(err)
		if !retry {
			return err
		}
		if waitErr := s.dependencies.Wait(ctx, delay); waitErr != nil {
			return fmt.Errorf("wait to retry delivery: %w", waitErr)
		}
	}
}

func retryDelay(err error) (time.Duration, bool) {
	var retryable interface {
		RetryAfter() time.Duration
	}
	if !errors.As(err, &retryable) {
		return 0, false
	}

	delay := retryable.RetryAfter()
	return delay, delay > 0
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
