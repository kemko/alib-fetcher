package telegram_test

import (
	"net/http"
	"net/url"
	"time"

	"github.com/kemko/alib-fetcher/internal/telegram"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func newTestClient(apiBase string) (*telegram.Client, error) {
	return newTestClientWithTimeout(apiBase, 2*time.Second)
}

func newTestClientWithTimeout(apiBase string, timeout time.Duration) (*telegram.Client, error) {
	target, err := url.Parse(apiBase)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport
	client := &http.Client{
		Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			routedRequest := request.Clone(request.Context())
			routedURL := *request.URL
			routedURL.Scheme = target.Scheme
			routedURL.Host = target.Host
			routedRequest.URL = &routedURL

			return transport.RoundTrip(routedRequest)
		}),
	}

	return telegram.NewClient(telegram.ClientConfig{
		Token:      "test-token",
		Timeout:    timeout,
		HTTPClient: client,
	})
}

func newTestSender(apiBase string) (*telegram.Sender, error) {
	return newTestSenderWithChat(apiBase, "-100123")
}

func newTestSenderWithChat(apiBase, chatID string) (*telegram.Sender, error) {
	client, err := newTestClient(apiBase)
	if err != nil {
		return nil, err
	}

	return client.NewSender(chatID)
}
