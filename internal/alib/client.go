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

// FetchWithResult downloads all configured pages and returns partial listing failures.
func (c *Client) FetchWithResult(ctx context.Context) (FetchResult, error) {
	downloaded, pageErrors, err := c.downloadPages(ctx)
	if err != nil {
		return FetchResult{}, err
	}
	books, failed, unidentifiedFailures, parsedPages, parseErrors, err := c.parsePages(ctx, downloaded)
	if err != nil {
		return FetchResult{}, err
	}
	pageErrors = append(pageErrors, parseErrors...)
	if parsedPages == 0 {
		return FetchResult{}, errors.Join(pageErrors...)
	}

	return FetchResult{
		Books:                books,
		FailedBuyURLs:        failed,
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

func (c *Client) parsePages(
	ctx context.Context,
	pages []downloadedPage,
) ([]Book, []string, int, int, []error, error) {
	books := make([]Book, 0)
	failed := make(map[string]struct{})
	pageErrors := make([]error, 0, len(pages))
	parsedPages := 0
	unidentifiedFailures := 0
	seen := make(map[string]struct{})
	failedOrder := make([]string, 0)
	for _, page := range pages {
		if err := ctx.Err(); err != nil {
			return nil, nil, 0, 0, nil, err
		}
		pageResult, err := ParseWithResult(bytes.NewReader(page.body), page.endpoint, page.contentType)
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, nil, 0, 0, nil, contextErr
		}
		if err != nil {
			if errors.Is(err, ErrNoBooks) {
				unidentifiedFailures += pageResult.UnidentifiedFailures
				mergeFailedListings(failed, seen, &failedOrder, pageResult.FailedBuyURLs)
			}
			pageURL := page.endpoint.String()
			pageErrors = append(pageErrors, fmt.Errorf("parse alib URL %q: %w", pageURL, err))
			c.logger.ErrorContext(ctx, "alib.page_parse_failed",
				slog.Int(logKeyIndex, page.index), slog.String(logKeyURL, pageURL), slog.Any(logKeyError, err))
			continue
		}
		parsedPages++
		c.logger.InfoContext(ctx, "alib.page_parsed",
			slog.Int(logKeyIndex, page.index), slog.String(logKeyURL, page.endpoint.String()),
			slog.Int(logKeyBooks, len(pageResult.Books)))
		unidentifiedFailures += pageResult.UnidentifiedFailures
		mergePageResult(&books, failed, seen, &failedOrder, pageResult)
	}

	return books, remainingFailures(failedOrder, failed), unidentifiedFailures, parsedPages, pageErrors, nil
}

func mergePageResult(
	books *[]Book,
	failed map[string]struct{},
	seen map[string]struct{},
	failedOrder *[]string,
	page ParseResult,
) {
	mergeFailedListings(failed, seen, failedOrder, page.FailedBuyURLs)
	for _, book := range page.Books {
		delete(failed, book.BuyURL)
		if _, exists := seen[book.BuyURL]; exists {
			continue
		}
		seen[book.BuyURL] = struct{}{}
		*books = append(*books, book)
	}
}

func mergeFailedListings(
	failed map[string]struct{},
	seen map[string]struct{},
	failedOrder *[]string,
	failedBuyURLs []string,
) {
	for _, buyURL := range failedBuyURLs {
		if _, succeeded := seen[buyURL]; succeeded {
			continue
		}
		if _, alreadyFailed := failed[buyURL]; !alreadyFailed {
			*failedOrder = append(*failedOrder, buyURL)
			failed[buyURL] = struct{}{}
		}
	}
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
