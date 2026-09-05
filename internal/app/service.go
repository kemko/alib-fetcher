// Package app coordinates the daily digest use case.
package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/digest"
)

// Fetcher obtains the latest source listings.
type Fetcher interface {
	FetchWithResult(context.Context) (alib.FetchResult, error)
}

// State tracks discovered listings and their delivery status.
type State interface {
	Prune(context.Context, time.Time) (int, error)
	Existing(context.Context, []alib.Book) ([]bool, error)
	RecordDiscovered(context.Context, []alib.Book, time.Time) (int, error)
	Pending(context.Context) ([]alib.Book, error)
	MarkSent(context.Context, []alib.Book, time.Time) error
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
	Failed  int
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

	fetchResult, err := s.dependencies.Fetcher.FetchWithResult(ctx)
	if err != nil {
		return result, fmt.Errorf("fetch listings: %w", err)
	}
	books := fetchResult.Books
	result.Fetched = len(books)
	failedURLs := make(map[string]struct{}, len(fetchResult.FailedBuyURLs))
	for _, buyURL := range fetchResult.FailedBuyURLs {
		failedURLs[buyURL] = struct{}{}
	}
	unidentifiedFailures := fetchResult.UnidentifiedFailures
	result.Failed = unidentifiedFailures + len(failedURLs)

	existing, err := s.existing(ctx, books)
	if err != nil {
		return result, err
	}
	recordableBooks, newBooks := recordableFetched(books, existing, failedURLs, renderOptions)
	result.Failed = unidentifiedFailures + len(failedURLs)
	if len(recordableBooks) > 0 {
		_, err = s.dependencies.State.RecordDiscovered(ctx, recordableBooks, cycleTime)
		if err != nil {
			return result, fmt.Errorf("record discovered listings: %w", err)
		}
	}
	result.New = newBooks

	pending, err := s.dependencies.State.Pending(ctx)
	if err != nil {
		return result, fmt.Errorf("load pending listings: %w", err)
	}
	pending = slices.DeleteFunc(sortPending(pending), func(book alib.Book) bool {
		_, failed := failedURLs[book.BuyURL]

		return failed
	})

	sent, renderedFailures, err := s.renderAndSend(ctx, pending, renderOptions, cycleTime, result.Failed)
	result.Sent = sent
	result.Failed = renderedFailures
	if err != nil {
		return result, err
	}

	return result, nil
}

func (s *Service) existing(ctx context.Context, books []alib.Book) ([]bool, error) {
	if len(books) == 0 {
		return nil, nil
	}
	existing, err := s.dependencies.State.Existing(ctx, books)
	if err != nil {
		return nil, fmt.Errorf("check existing listings: %w", err)
	}
	if len(existing) != len(books) {
		return nil, errors.New("check existing listings: state returned an invalid result")
	}

	return existing, nil
}

func (s *Service) renderAndSend(
	ctx context.Context,
	pending []alib.Book,
	options digest.Options,
	cycleTime time.Time,
	previousFailures int,
) (int, int, error) {
	chunks, skippedBuyURLs, err := digest.RenderSendable(pending, options, previousFailures)
	if err != nil {
		return 0, previousFailures, fmt.Errorf("render digest: %w", err)
	}
	if len(chunks) > 0 && s.dependencies.BeforeDelivery != nil {
		if hookErr := s.dependencies.BeforeDelivery(ctx); hookErr != nil {
			return 0, previousFailures + len(skippedBuyURLs), fmt.Errorf("prepare digest delivery: %w", hookErr)
		}
	}
	ackCtx := context.WithoutCancel(ctx)
	sent := 0
	for index, chunk := range chunks {
		silent := index < len(chunks)-1
		attachRefresh := index == len(chunks)-1
		if sendErr := s.send(ctx, chunk.Text, silent, attachRefresh); sendErr != nil {
			return sent, previousFailures + len(skippedBuyURLs), fmt.Errorf("send digest: %w", sendErr)
		}
		if len(chunk.Books) == 0 {
			continue
		}
		if markErr := s.dependencies.State.MarkSent(ackCtx, chunk.Books, cycleTime); markErr != nil {
			return sent, previousFailures + len(skippedBuyURLs), fmt.Errorf("record delivered listings: %w", markErr)
		}
		sent += len(chunk.Books)
	}

	return sent, previousFailures + len(skippedBuyURLs), nil
}

func recordableFetched(
	books []alib.Book,
	existing []bool,
	failedURLs map[string]struct{},
	options digest.Options,
) ([]alib.Book, int) {
	recordable := make([]alib.Book, 0, len(books))
	newBooks := 0
	for index, book := range books {
		if existing[index] {
			recordable = append(recordable, book)
			continue
		}

		if !renderable(book, options) {
			failedURLs[book.BuyURL] = struct{}{}
			continue
		}
		recordable = append(recordable, book)
		newBooks++
	}

	return recordable, newBooks
}

func renderable(book alib.Book, options digest.Options) bool {
	_, err := digest.RenderBook(book, options)

	return err == nil
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
