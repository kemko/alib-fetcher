package alib

import (
	"bytes"
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
	maxPageResponseBytes = 4 << 20
	logKeyError          = "error"
	logKeyIndex          = "index"
	logKeyURL            = "url"
	logKeyBooks          = "books"
)

// ErrUnexpectedStatus indicates that Alib.ru did not return an HTTP 200 response.
var ErrUnexpectedStatus = errors.New("alib returned an unexpected status")

var errResponseTooLarge = fmt.Errorf("alib response exceeds %d bytes", maxPageResponseBytes)

// FetchResult contains successfully fetched books and failed listing identities.
type FetchResult struct {
	Books                []Book
	FailedBuyURLs        []string
	UnidentifiedFailures int
}

// Client fetches book listings from configured Alib.ru pages.
type Client struct {
	httpClient      *http.Client
	logger          *slog.Logger
	endpoints       []*url.URL
	requestInterval time.Duration
}

type downloadedPage struct {
	endpoint    *url.URL
	contentType string
	body        []byte
	index       int
}

// NewClient builds an Alib.ru client with a bounded request timeout.
func NewClient(rawURLs []string, timeout, requestInterval time.Duration, logger *slog.Logger) (*Client, error) {
	if timeout <= 0 {
		return nil, errors.New("create alib client: timeout must be positive")
	}
	if requestInterval < 0 {
		return nil, errors.New("create alib client: request interval must be non-negative")
	}
	if logger == nil {
		return nil, errors.New("create alib client: logger is required")
	}
	if len(rawURLs) == 0 {
		return nil, errors.New("create alib client: at least one URL is required")
	}

	endpoints := make([]*url.URL, 0, len(rawURLs))
	for index, rawURL := range rawURLs {
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

// FetchWithResult downloads all configured pages and returns partial listing failures.
func (c *Client) FetchWithResult(ctx context.Context) (FetchResult, error) {
	downloaded, pageErrors, downloadErr := c.downloadPages(ctx)
	if downloadErr != nil {
		return FetchResult{}, downloadErr
	}
	state := parseState{
		books:       make([]Book, 0),
		seen:        make(map[string]struct{}),
		failed:      make(map[string]struct{}),
		failedOrder: make([]string, 0),
	}
	parsedPages := 0
	unidentifiedFailures := 0
	for _, page := range downloaded {
		if err := ctx.Err(); err != nil {
			return FetchResult{}, err
		}
		pageResult, parseErr := ParseWithResult(bytes.NewReader(page.body), page.endpoint, page.contentType)
		if contextErr := ctx.Err(); contextErr != nil {
			return FetchResult{}, contextErr
		}
		if parseErr != nil {
			if errors.Is(parseErr, ErrNoBooks) {
				unidentifiedFailures += pageResult.UnidentifiedFailures
				for _, buyURL := range pageResult.FailedBuyURLs {
					state.addFailure(buyURL)
				}
			}
			pageURL := page.endpoint.String()
			pageErrors = append(pageErrors, fmt.Errorf("parse alib URL %q: %w", pageURL, parseErr))
			c.logger.ErrorContext(ctx, "alib.page_parse_failed",
				slog.Int(logKeyIndex, page.index), slog.String(logKeyURL, pageURL), slog.Any(logKeyError, parseErr))
			continue
		}
		parsedPages++
		c.logger.InfoContext(ctx, "alib.page_parsed",
			slog.Int(logKeyIndex, page.index), slog.String(logKeyURL, page.endpoint.String()),
			slog.Int(logKeyBooks, len(pageResult.Books)))
		unidentifiedFailures += pageResult.UnidentifiedFailures
		for _, buyURL := range pageResult.FailedBuyURLs {
			state.addFailure(buyURL)
		}
		for _, book := range pageResult.Books {
			state.addBook(book)
		}
	}
	if parsedPages == 0 {
		return FetchResult{}, errors.Join(pageErrors...)
	}

	return FetchResult{
		Books:                state.books,
		FailedBuyURLs:        state.failedBuyURLs(),
		UnidentifiedFailures: unidentifiedFailures,
	}, nil
}

func (c *Client) downloadPages(ctx context.Context) ([]downloadedPage, []error, error) {
	downloaded := make([]downloadedPage, 0, len(c.endpoints))
	pageErrors := make([]error, 0, len(c.endpoints))
	for index, endpoint := range c.endpoints {
		page, err := c.downloadPage(ctx, index, endpoint)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			pageURL := endpoint.String()
			pageErrors = append(pageErrors, fmt.Errorf("download alib URL %q: %w", pageURL, err))
			c.logger.ErrorContext(ctx, "alib.page_download_failed",
				slog.Int(logKeyIndex, index), slog.String(logKeyURL, pageURL), slog.Any(logKeyError, err))
		} else {
			downloaded = append(downloaded, page)
			c.logger.InfoContext(ctx, "alib.page_downloaded",
				slog.Int(logKeyIndex, index), slog.String(logKeyURL, endpoint.String()))
		}
		if index < len(c.endpoints)-1 {
			if waitErr := wait(ctx, c.requestInterval); waitErr != nil {
				return nil, nil, waitErr
			}
		}
	}

	return downloaded, pageErrors, nil
}

func (c *Client) downloadPage(ctx context.Context, index int, endpoint *url.URL) (downloadedPage, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return downloadedPage{}, fmt.Errorf("create alib request %d: %w", index, err)
	}
	request.Header.Set("User-Agent", "alib-fetcher/1.0")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return downloadedPage{}, fmt.Errorf("fetch alib page: %w", urlErrorCause(err))
	}

	if response.StatusCode != http.StatusOK {
		fetchErr := fmt.Errorf("%w: %s", ErrUnexpectedStatus, response.Status)
		if closeErr := response.Body.Close(); closeErr != nil {
			fetchErr = errors.Join(fetchErr, fmt.Errorf("close alib response: %w", closeErr))
		}
		return downloadedPage{}, fetchErr
	}
	if response.ContentLength > maxPageResponseBytes {
		responseErr := errResponseTooLarge
		if closeErr := response.Body.Close(); closeErr != nil {
			responseErr = errors.Join(responseErr, fmt.Errorf("close alib response: %w", closeErr))
		}
		return downloadedPage{}, responseErr
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxPageResponseBytes+1))
	if err == nil && len(body) > maxPageResponseBytes {
		err = errResponseTooLarge
	}
	closeErr := response.Body.Close()
	if err != nil || closeErr != nil {
		if err != nil {
			err = fmt.Errorf("read alib response: %w", err)
		}
		if closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close alib response: %w", closeErr))
		}
		return downloadedPage{}, err
	}

	return downloadedPage{
		endpoint:    endpoint,
		body:        body,
		contentType: response.Header.Get("Content-Type"),
		index:       index,
	}, nil
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
