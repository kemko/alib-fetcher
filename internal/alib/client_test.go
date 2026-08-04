package alib_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kemmko/alib-fetcher/internal/alib"
	"github.com/stretchr/testify/require"
)

func Test_Client_fetches_and_parses_page(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "alib-fetcher/1.0", request.Header.Get("User-Agent"))
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err := writer.Write([]byte(`<p><b>Книга.</b> Цена: 100 руб. <a href="/book.html"><b>Купить</b></a></p>`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	client, err := alib.NewClient(server.URL, time.Second)
	require.NoError(t, err)

	// When
	books, err := client.Fetch(context.Background())

	// Then
	require.NoError(t, err)
	require.Equal(t, []alib.Book{{Title: "Книга.", Price: "100 руб.", BuyURL: server.URL + "/book.html"}}, books)
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
