package alib

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	logKeyBooks = "books"
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

//nolint:govet // Keep endpoint, body, and content type together between phases.
type downloadedPage struct {
	index       int
	endpoint    *url.URL
	body        string
	contentType string
}

// NewClient builds an Alib.ru client with a bounded request timeout.
func NewClient(rawURLs string, timeout, requestInterval time.Duration, logger *slog.Logger) (*Client, error) {
	if timeout <= 0 {
		return nil, errors.New("create alib client: timeout must be positive")
	}
	if requestInterval < 0 {
		return nil, errors.New("create alib client: request interval must be non-negative")
	}
	if logger == nil {
		return nil, errors.New("create alib client: logger is required")
	}

	endpoints := make([]*url.URL, 0)
	for index, rawURL := range strings.Split(rawURLs, ",") {
		rawURL = strings.TrimSpace(rawURL)
		if rawURL == "" {
			return nil, fmt.Errorf("parse alib URL item %d: URL is empty", index)
		}

		endpoint, err := url.Parse(rawURL)
		if err != nil {
			return nil, fmt.Errorf("parse alib URL item %d: %w", index, urlErrorCause(err))
		}
		if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
			return nil, fmt.Errorf("parse alib URL item %d: unsupported scheme %q", index, endpoint.Scheme)
		}
		if endpoint.Host == "" {
			return nil, fmt.Errorf("parse alib URL item %d: host is required", index)
		}
		if endpoint.User != nil {
			return nil, fmt.Errorf("parse alib URL item %d: userinfo is not supported", index)
		}

		endpoints = append(endpoints, endpoint)
	}

	return &Client{
		httpClient:      &http.Client{Timeout: timeout},
		endpoints:       endpoints,
		requestInterval: requestInterval,
		logger:          logger,
	}, nil
}

// Fetch downloads all configured pages and then parses successful responses in order.
func (c *Client) Fetch(ctx context.Context) ([]Book, error) {
	downloaded := make([]downloadedPage, 0, len(c.endpoints))
	pageErrors := make([]error, 0, len(c.endpoints))

	for index, endpoint := range c.endpoints {
		page, err := c.downloadPage(ctx, index, endpoint)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}

			safeURL := endpointForLog(endpoint)
			pageErr := fmt.Errorf("download alib URL %q: %w", safeURL, err)
			pageErrors = append(pageErrors, pageErr)
			c.logger.ErrorContext(ctx, "alib.page_download_failed",
				slog.Int(logKeyIndex, index),
				slog.String(logKeyURL, safeURL),
				slog.Any(logKeyError, err),
			)
		} else {
			downloaded = append(downloaded, *page)
			c.logger.InfoContext(ctx, "alib.page_downloaded",
				slog.Int(logKeyIndex, index),
				slog.String(logKeyURL, endpointForLog(endpoint)),
			)
		}

		if index == len(c.endpoints)-1 {
			break
		}
		if waitErr := wait(ctx, c.requestInterval); waitErr != nil {
			return nil, waitErr
		}
	}

	books := make([]Book, 0)
	seen := make(map[string]struct{})
	parsedPages := 0
	for _, page := range downloaded {
		pageBooks, err := Parse(strings.NewReader(page.body), page.endpoint, page.contentType)
		if err != nil {
			pageErr := fmt.Errorf("parse alib URL %q: %w", endpointForLog(page.endpoint), err)
			pageErrors = append(pageErrors, pageErr)
			c.logger.ErrorContext(ctx, "alib.page_parse_failed",
				slog.Int(logKeyIndex, page.index),
				slog.String(logKeyURL, endpointForLog(page.endpoint)),
				slog.Any(logKeyError, err),
			)
			continue
		}

		parsedPages++
		c.logger.InfoContext(ctx, "alib.page_parsed",
			slog.Int(logKeyIndex, page.index),
			slog.String(logKeyURL, endpointForLog(page.endpoint)),
			slog.Int(logKeyBooks, len(pageBooks)),
		)
		for _, book := range pageBooks {
			if _, exists := seen[book.BuyURL]; exists {
				continue
			}

			seen[book.BuyURL] = struct{}{}
			books = append(books, book)
		}
	}

	if parsedPages == 0 {
		return nil, errors.Join(pageErrors...)
	}

	return books, nil
}

func (c *Client) downloadPage(ctx context.Context, index int, endpoint *url.URL) (*downloadedPage, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create alib request %d: %w", index, err)
	}
	request.Header.Set("User-Agent", "alib-fetcher/1.0")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch alib page: %w", urlErrorCause(err))
	}

	if response.StatusCode != http.StatusOK {
		fetchErr := fmt.Errorf("%w: %s", ErrUnexpectedStatus, response.Status)
		if closeErr := response.Body.Close(); closeErr != nil {
			fetchErr = errors.Join(fetchErr, fmt.Errorf("close alib response: %w", closeErr))
		}
		return nil, fetchErr
	}

	body, err := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err != nil || closeErr != nil {
		if err != nil {
			err = fmt.Errorf("read alib response: %w", err)
		}
		if closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close alib response: %w", closeErr))
		}
		return nil, err
	}

	return &downloadedPage{
		index:       index,
		endpoint:    endpoint,
		body:        string(body),
		contentType: response.Header.Get("Content-Type"),
	}, nil
}

func endpointForLog(endpoint *url.URL) string {
	safeEndpoint := *endpoint
	safeEndpoint.User = nil
	safeEndpoint.RawQuery = ""
	safeEndpoint.ForceQuery = false
	safeEndpoint.Fragment = ""

	return safeEndpoint.String()
}

type redactedURLError struct {
	cause error
}

func (err redactedURLError) Error() string {
	return "URL operation failed"
}

func (err redactedURLError) Unwrap() error {
	return err.cause
}

func urlErrorCause(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return redactedURLError{cause: urlErr.Err}
	}

	return err
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
