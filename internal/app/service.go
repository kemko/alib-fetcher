package app

import (
	"context"
	"fmt"

	"github.com/kemmko/alib-fetcher/internal/alib"
	"github.com/kemmko/alib-fetcher/internal/digest"
)

// Fetcher obtains the latest source listings.
type Fetcher interface {
	Fetch(context.Context) ([]alib.Book, error)
}

// State tracks which listings have already been delivered.
type State interface {
	Unseen(context.Context, []alib.Book) ([]alib.Book, error)
	MarkSent(context.Context, []alib.Book) error
}

// Sender delivers one rendered message.
type Sender interface {
	Send(context.Context, string) error
}

// Dependencies contains the service adapters and Telegram message limit.
type Dependencies struct {
	Fetcher      Fetcher
	State        State
	Sender       Sender
	MessageLimit int
}

// Result summarizes one completed or partially completed fetch job.
type Result struct {
	Fetched int
	New     int
	Sent    int
}

// Service coordinates fetching, deduplication, delivery, and acknowledgement.
type Service struct {
	dependencies Dependencies
}

// NewService builds the daily digest service.
func NewService(dependencies Dependencies) *Service {
	return &Service{dependencies: dependencies}
}

// Run executes one complete digest cycle.
func (s *Service) Run(ctx context.Context) (Result, error) {
	books, err := s.dependencies.Fetcher.Fetch(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("fetch listings: %w", err)
	}
	result := Result{Fetched: len(books)}

	unseen, err := s.dependencies.State.Unseen(ctx, books)
	if err != nil {
		return result, fmt.Errorf("filter listings: %w", err)
	}
	result.New = len(unseen)
	if len(unseen) == 0 {
		return result, nil
	}

	chunks, err := digest.Render(unseen, s.dependencies.MessageLimit)
	if err != nil {
		return result, fmt.Errorf("render digest: %w", err)
	}
	for _, chunk := range chunks {
		if err := s.dependencies.Sender.Send(ctx, chunk.Text); err != nil {
			return result, fmt.Errorf("send digest: %w", err)
		}
		if err := s.dependencies.State.MarkSent(ctx, chunk.Books); err != nil {
			return result, fmt.Errorf("record delivered listings: %w", err)
		}
		result.Sent += len(chunk.Books)
	}

	return result, nil
}
