// Package app coordinates the daily digest use case.
package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/digest"
	"github.com/kemko/alib-fetcher/internal/slink"
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
	if s.dependencies.PhotoProcessor != nil {
		renderOptions.SlinkProfile = s.dependencies.PhotoProcessor.Profile()
	}

	existing, err := s.existing(ctx, books)
	if err != nil {
		return result, err
	}
	preparedBooks, preparedBooksByURL, newBooks, err := s.prepareFetched(
		ctx,
		books,
		existing,
		failedURLs,
		renderOptions,
	)
	result.Failed = unidentifiedFailures + len(failedURLs)
	if err != nil {
		return result, err
	}
	if len(preparedBooks) > 0 {
		_, err = s.dependencies.State.RecordDiscovered(ctx, preparedBooks, cycleTime)
		if err != nil {
			return result, fmt.Errorf("record discovered listings: %w", err)
		}
	}
	result.New = newBooks

	pending, err := s.dependencies.State.Pending(ctx)
	if err != nil {
		return result, fmt.Errorf("load pending listings: %w", err)
	}
	pending = sortPending(pending)
	pending, err = s.preparePending(ctx, pending, preparedBooksByURL, failedURLs, &renderOptions)
	result.Failed = unidentifiedFailures + len(failedURLs)
	if err != nil {
		return result, err
	}

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
		silent := index > 0
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

func (s *Service) prepareFetched(
	ctx context.Context,
	books []alib.Book,
	existing []bool,
	failedURLs map[string]struct{},
	options digest.Options,
) ([]alib.Book, map[string]alib.Book, int, error) {
	prepared := make([]alib.Book, 0, len(books))
	preparedBooks := make(map[string]alib.Book, len(books))
	newBooks := 0
	for index, book := range books {
		if existing[index] {
			prepared = append(prepared, book)
			continue
		}

		preparedBook, cleanup, err := s.prepareBook(ctx, book)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, 0, fmt.Errorf("prepare book %q: %w", book.BuyURL, err)
			}
			failedURLs[book.BuyURL] = struct{}{}
			continue
		}
		if !renderable(preparedBook, options) {
			if cleanupErr := cleanup(); cleanupErr != nil && ctx.Err() != nil {
				return nil, nil, 0, fmt.Errorf("cleanup prepared book %q: %w", book.BuyURL, cleanupErr)
			}
			failedURLs[book.BuyURL] = struct{}{}
			continue
		}
		if cleanupErr := cleanup(); cleanupErr != nil {
			if ctx.Err() != nil {
				return nil, nil, 0, fmt.Errorf("cleanup prepared book %q: %w", book.BuyURL, cleanupErr)
			}
			failedURLs[book.BuyURL] = struct{}{}
			continue
		}
		prepared = append(prepared, preparedBook)
		preparedBooks[book.BuyURL] = preparedBook
		newBooks++
	}

	return prepared, preparedBooks, newBooks, nil
}

func (s *Service) preparePending(
	ctx context.Context,
	pending []alib.Book,
	preparedBooks map[string]alib.Book,
	failedURLs map[string]struct{},
	options *digest.Options,
) ([]alib.Book, error) {
	result := make([]alib.Book, 0, len(pending))
	for _, book := range pending {
		preparedBook, ready, err := s.preparePendingBook(ctx, book, preparedBooks, failedURLs, *options)
		if err != nil {
			return nil, err
		}
		if ready {
			result = append(result, preparedBook)
		}
	}

	return result, nil
}

func (s *Service) preparePendingBook(
	ctx context.Context,
	book alib.Book,
	preparedBooks map[string]alib.Book,
	failedURLs map[string]struct{},
	options digest.Options,
) (alib.Book, bool, error) {
	if preparedBook, alreadyProcessed := preparedBooks[book.BuyURL]; alreadyProcessed {
		return preparedBook, true, nil
	}
	if _, alreadyFailed := failedURLs[book.BuyURL]; alreadyFailed {
		return alib.Book{}, false, nil
	}

	preparedBook, cleanup, err := s.prepareBook(ctx, book)
	if err != nil {
		if ctx.Err() != nil {
			return alib.Book{}, false, fmt.Errorf("prepare book %q: %w", book.BuyURL, err)
		}
		failedURLs[book.BuyURL] = struct{}{}
		return alib.Book{}, false, nil
	}
	if !reflect.DeepEqual(book, preparedBook) {
		if saveErr := s.dependencies.State.SavePrepared(ctx, preparedBook); saveErr != nil {
			return alib.Book{}, false, fmt.Errorf(
				"save prepared book %q: %w", book.BuyURL, errors.Join(saveErr, cleanup()))
		}
	}
	if !renderable(preparedBook, options) {
		if cleanupErr := cleanup(); cleanupErr != nil && ctx.Err() != nil {
			return alib.Book{}, false, fmt.Errorf("cleanup prepared book %q: %w", book.BuyURL, cleanupErr)
		}
		failedURLs[book.BuyURL] = struct{}{}
		return alib.Book{}, false, nil
	}
	if cleanupErr := cleanup(); cleanupErr != nil {
		if ctx.Err() != nil {
			return alib.Book{}, false, fmt.Errorf("cleanup prepared book %q: %w", book.BuyURL, cleanupErr)
		}
		failedURLs[book.BuyURL] = struct{}{}
		return alib.Book{}, false, nil
	}

	return preparedBook, true, nil
}

func (s *Service) prepareBook(ctx context.Context, book alib.Book) (alib.Book, func() error, error) {
	if len(book.Photos) == 0 || s.dependencies.PhotoProcessor == nil {
		return book, func() error { return nil }, nil
	}

	prepared, err := s.dependencies.PhotoProcessor.Process(ctx, book)
	if prepared != nil {
		if err != nil {
			if cleanupErr := prepared.Cleanup(); cleanupErr != nil {
				err = errors.Join(err, cleanupErr)
			}
			return book, func() error { return nil }, err
		}
	}
	if err != nil {
		return book, func() error { return nil }, err
	}
	if prepared == nil {
		return book, func() error { return nil }, errors.New("photo processor returned no book")
	}
	if prepared.Book.BuyURL == "" {
		prepared.Book.BuyURL = book.BuyURL
	}

	return prepared.Book, prepared.Cleanup, nil
}

func renderable(book alib.Book, options digest.Options) bool {
	_, skipped, err := digest.RenderSendable([]alib.Book{book}, options, 0)

	return err == nil && len(skipped) == 0
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
