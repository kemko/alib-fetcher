package alib_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kemko/alib-fetcher/internal/alib"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_Client_fetches_and_parses_page(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "alib-fetcher/1.0", request.Header.Get("User-Agent"))
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err := writer.Write([]byte(`<p><b>Книга.</b> Цена: 100 руб. <a href="/book.html"><b>Купить</b></a></p>`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	client, err := alib.NewClient(server.URL, time.Second)
	require.NoError(t, err)

	// When
	books, err := client.Fetch(context.Background())

	// Then
	require.NoError(t, err)
	require.Equal(t, []alib.Book{{
		Title:  "Книга.",
		Price:  "100 руб.",
		BuyURL: server.URL + "/book.html",
	}}, books)
}

func Test_Client_rejects_non_success_status(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	client, err := alib.NewClient(server.URL, time.Second)
	require.NoError(t, err)

	// When
	books, err := client.Fetch(context.Background())

	// Then
	require.ErrorIs(t, err, alib.ErrUnexpectedStatus)
	require.Empty(t, books)
}

func Test_Client_returns_parse_error_for_structurally_changed_page(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err := writer.Write([]byte(`<html><body>No listings here</body></html>`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	client, err := alib.NewClient(server.URL, time.Second)
	require.NoError(t, err)

	// When
	books, err := client.Fetch(context.Background())

	// Then
	require.ErrorIs(t, err, alib.ErrNoBooks)
	require.Empty(t, books)
}

func Test_Client_returns_context_error_when_request_is_canceled(t *testing.T) {
	t.Parallel()

	// Given
	client, err := alib.NewClient("https://www.alib.ru/tramka.phtml?tnew=7", time.Second)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// When
	books, err := client.Fetch(ctx)

	// Then
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, books)
}

func Test_NewClient_validates_configuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		rawURL string
	}{
		{
			name:   "unsupported scheme",
			rawURL: "file:///tmp/alib.html",
		},
		{
			name:   "missing host",
			rawURL: "https:///tramkaa.phtml",
		},
		{
			name:   "malformed URL",
			rawURL: "://",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// When
			client, err := alib.NewClient(tt.rawURL, time.Second)

			// Then
			require.Error(t, err)
			require.Nil(t, client)
		})
	}
}

func Test_NewClient_rejects_non_positive_timeout(t *testing.T) {
	t.Parallel()

	// When
	client, err := alib.NewClient("https://www.alib.ru/tramka.phtml?tnew=7", 0)

	// Then
	require.Error(t, err)
	require.Nil(t, client)
}
