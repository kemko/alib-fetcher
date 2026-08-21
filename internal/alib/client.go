package alib

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	logKeyError = "error"
	logKeyIndex = "index"
	logKeyURL   = "url"
)

// ErrUnexpectedStatus indicates that Alib.ru did not return an HTTP 200 response.
var ErrUnexpectedStatus = errors.New("alib returned an unexpected status")

// Client fetches book listings from configured Alib.ru pages.
type Client struct {
	httpClient      *http.Client
	logger          *slog.Logger
	endpoints       []*url.URL
	requestInterval time.Duration
}

// NewClient builds an Alib.ru client with a bounded request timeout.
func NewClient(rawURLs string, timeout, requestInterval time.Duration, logger *slog.Logger) (*Client, error) {
	if timeout <= 0 {
		return nil, errors.New("create alib client: timeout must be positive")
	}
	if requestInterval < 0 {
		return nil, errors.New("create alib client: request interval must be non-negative")
	}

	endpoints := make([]*url.URL, 0)
	for index, rawURL := range strings.Split(rawURLs, ",") {
		rawURL = strings.TrimSpace(rawURL)
		if rawURL == "" {
			return nil, fmt.Errorf("parse alib URL item %d: URL is empty", index)
		}

		endpoint, err := url.Parse(rawURL)
		if err != nil {
			return nil, fmt.Errorf("parse alib URL item %d %q: %w", index, rawURL, err)
		}
		if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
			return nil, fmt.Errorf("parse alib URL item %d %q: unsupported scheme %q", index, rawURL, endpoint.Scheme)
		}
		if endpoint.Host == "" {
			return nil, fmt.Errorf("parse alib URL item %d %q: host is required", index, rawURL)
		}

		endpoints = append(endpoints, endpoint)
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &Client{
		httpClient:      &http.Client{Timeout: timeout},
		endpoints:       endpoints,
		requestInterval: requestInterval,
		logger:          logger,
	}, nil
}

// Fetch downloads and parses configured listings pages in order.
func (c *Client) Fetch(ctx context.Context) ([]Book, error) {
	books := make([]Book, 0)
	seen := make(map[string]struct{})
	pageErrors := make([]error, 0, len(c.endpoints))

	for index, endpoint := range c.endpoints {
		pageBooks, err := c.fetchPage(ctx, endpoint)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}

			pageErr := fmt.Errorf("fetch alib URL %q: %w", endpoint.String(), err)
			pageErrors = append(pageErrors, pageErr)
			c.logger.ErrorContext(ctx, "alib.page_failed",
				slog.Int(logKeyIndex, index),
				slog.String(logKeyURL, endpoint.String()),
				slog.Any(logKeyError, err),
			)
		} else {
			for _, book := range pageBooks {
				if _, exists := seen[book.BuyURL]; exists {
					continue
				}

				seen[book.BuyURL] = struct{}{}
				books = append(books, book)
			}
		}

		if index == len(c.endpoints)-1 {
			break
		}
		if waitErr := wait(ctx, c.requestInterval); waitErr != nil {
			return nil, waitErr
		}
	}

	if len(pageErrors) == len(c.endpoints) {
		return nil, errors.Join(pageErrors...)
	}

	return books, nil
}

func (c *Client) fetchPage(ctx context.Context, endpoint *url.URL) ([]Book, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create alib request: %w", err)
	}
	request.Header.Set("User-Agent", "alib-fetcher/1.0")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch alib page: %w", err)
	}

	if response.StatusCode != http.StatusOK {
		fetchErr := fmt.Errorf("%w: %s", ErrUnexpectedStatus, response.Status)
		if closeErr := response.Body.Close(); closeErr != nil {
			fetchErr = errors.Join(fetchErr, fmt.Errorf("close alib response: %w", closeErr))
		}
		return nil, fetchErr
	}

	books, fetchErr := Parse(response.Body, endpoint, response.Header.Get("Content-Type"))
	if closeErr := response.Body.Close(); closeErr != nil {
		fetchErr = errors.Join(fetchErr, fmt.Errorf("close alib response: %w", closeErr))
	}
	if fetchErr != nil {
		return nil, fmt.Errorf("parse alib response: %w", fetchErr)
	}

	return books, nil
}

func wait(ctx context.Context, duration time.Duration) error {
	if duration == 0 {
		return nil
	}

	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
