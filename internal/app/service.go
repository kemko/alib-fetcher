// Package app coordinates the daily digest use case.
package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/digest"
	"github.com/kemko/alib-fetcher/internal/slink"
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
	SavePrepared(context.Context, alib.Book) error
	MarkSent(context.Context, []alib.Book, time.Time) error
}

// PhotoProcessor prepares a book's source photos for digest rendering.
type PhotoProcessor interface {
	Process(context.Context, alib.Book) (*slink.PreparedBook, error)
	Profile() string
}

// Sender delivers one rendered message.
type Sender interface {
	Send(ctx context.Context, text string, silent bool, attachRefresh bool) error
}

// FreshBooksPolicy resolves an inclusive publication-year threshold for one cycle year.
type FreshBooksPolicy interface {
	LowerYear(currentYear int) int
}

// Dependencies contains the service adapters and digest policy.
type Dependencies struct {
	Fetcher        Fetcher
	State          State
	Sender         Sender
	FreshBooks     FreshBooksPolicy
	PhotoProcessor PhotoProcessor
	Location       *time.Location
	Now            func() time.Time
	Wait           func(context.Context, time.Duration) error
	BeforeDelivery func(context.Context) error
	MessageLimit   int
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
	renderOptions := s.renderOptions(cycleTime)
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

	pending = sortPending(pending)
	if prepareErr := s.preparePhotos(ctx, pending, &renderOptions); prepareErr != nil {
		return result, prepareErr
	}

	sent, err := s.renderAndSend(ctx, pending, renderOptions, cycleTime)
	result.Sent = sent
	if err != nil {
		return result, err
	}

	return result, nil
}

func (s *Service) renderAndSend(
	ctx context.Context,
	pending []alib.Book,
	options digest.Options,
	cycleTime time.Time,
) (int, error) {
	chunks, skippedBuyURLs, err := digest.RenderSendable(pending, options)
	if err != nil {
		return 0, fmt.Errorf("render digest: %w", err)
	}
	if len(chunks) > 0 && s.dependencies.BeforeDelivery != nil {
		if hookErr := s.dependencies.BeforeDelivery(ctx); hookErr != nil {
			return 0, fmt.Errorf("prepare digest delivery: %w", hookErr)
		}
	}
	ackCtx := context.WithoutCancel(ctx)
	sent := 0
	for index, chunk := range chunks {
		silent := index > 0
		attachRefresh := index == len(chunks)-1
		if sendErr := s.send(ctx, chunk.Text, silent, attachRefresh); sendErr != nil {
			return sent, fmt.Errorf("send digest: %w", sendErr)
		}
		if len(chunk.Books) == 0 {
			continue
		}
		if markErr := s.dependencies.State.MarkSent(ackCtx, chunk.Books, cycleTime); markErr != nil {
			return sent, fmt.Errorf("record delivered listings: %w", markErr)
		}
		sent += len(chunk.Books)
	}
	if len(skippedBuyURLs) > 0 {
		return sent, fmt.Errorf("render digest: %w: %s", digest.ErrMessageTooLong, strings.Join(skippedBuyURLs, ", "))
	}

	return sent, nil
}

func (s *Service) preparePhotos(ctx context.Context, pending []alib.Book, options *digest.Options) error {
	if s.dependencies.PhotoProcessor == nil {
		return nil
	}
	options.SlinkProfile = s.dependencies.PhotoProcessor.Profile()
	for index := range pending {
		book := pending[index]
		if len(book.Photos) == 0 {
			continue
		}

		prepared, err := s.dependencies.PhotoProcessor.Process(ctx, book)
		if err != nil {
			if prepared != nil {
				err = errors.Join(err, prepared.Cleanup())
			}

			return fmt.Errorf("prepare photos for %q: %w", book.BuyURL, err)
		}
		if prepared == nil {
			return fmt.Errorf("prepare photos for %q: processor returned no book", book.BuyURL)
		}

		preparedBook := prepared.Book
		if !reflect.DeepEqual(book, preparedBook) {
			if saveErr := s.dependencies.State.SavePrepared(ctx, preparedBook); saveErr != nil {
				return fmt.Errorf("save prepared book %q: %w", book.BuyURL, errors.Join(saveErr, prepared.Cleanup()))
			}
		}
		if cleanupErr := prepared.Cleanup(); cleanupErr != nil {
			return fmt.Errorf("cleanup prepared photos for %q: %w", book.BuyURL, cleanupErr)
		}
		pending[index] = preparedBook
	}

	return nil
}

func sortPending(pending []alib.Book) []alib.Book {
	pending = append([]alib.Book(nil), pending...)
	sort.SliceStable(pending, func(i, j int) bool {
		left, right := pending[i].PublicationYear, pending[j].PublicationYear
		if left == 0 {
			return right != 0
		}
		if right == 0 {
			return false
		}

		return left > right
	})

	return pending
}

func (s *Service) renderOptions(cycleTime time.Time) digest.Options {
	localTime := cycleTime
	if s.dependencies.Location != nil {
		localTime = cycleTime.In(s.dependencies.Location)
	}

	options := digest.Options{
		LocalTime: localTime,
		Limit:     s.dependencies.MessageLimit,
	}
	if s.dependencies.FreshBooks != nil {
		lowerYear := s.dependencies.FreshBooks.LowerYear(localTime.Year())
		options.FreshBooksLowerYear = &lowerYear
	}

	return options
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
