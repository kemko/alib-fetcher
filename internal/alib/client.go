package alib

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// ErrUnexpectedStatus indicates that Alib.ru did not return an HTTP 200 response.
var ErrUnexpectedStatus = errors.New("alib returned an unexpected status")

// Client fetches book listings from a configured Alib.ru page.
type Client struct {
	httpClient *http.Client
	endpoint   *url.URL
}

// NewClient builds an Alib.ru client with a bounded request timeout.
func NewClient(rawURL string, timeout time.Duration) (*Client, error) {
	endpoint, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse alib URL: %w", err)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("parse alib URL: unsupported scheme %q", endpoint.Scheme)
	}
	if endpoint.Host == "" {
		return nil, errors.New("parse alib URL: host is required")
	}
	if timeout <= 0 {
		return nil, errors.New("create alib client: timeout must be positive")
	}

	return &Client{
		httpClient: &http.Client{Timeout: timeout},
		endpoint:   endpoint,
	}, nil
}

// Fetch downloads and parses the current listings page.
func (c *Client) Fetch(ctx context.Context) (books []Book, fetchErr error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create alib request: %w", err)
	}
	request.Header.Set("User-Agent", "alib-fetcher/1.0")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch alib page: %w", err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			fetchErr = errors.Join(fetchErr, fmt.Errorf("close alib response: %w", closeErr))
		}
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s", ErrUnexpectedStatus, response.Status)
	}

	books, err = Parse(response.Body, c.endpoint, response.Header.Get("Content-Type"))
	if err != nil {
		return nil, fmt.Errorf("parse alib response: %w", err)
	}

	return books, nil
}
