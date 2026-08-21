package alib_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
	client, err := alib.NewClient(server.URL, time.Second, 0, slog.New(slog.DiscardHandler))
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
	client, err := alib.NewClient(server.URL, time.Second, 0, slog.New(slog.DiscardHandler))
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
	client, err := alib.NewClient(server.URL, time.Second, 0, slog.New(slog.DiscardHandler))
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
	client, err := alib.NewClient(
		"https://www.alib.ru/tramka.phtml?tnew=7",
		time.Second,
		0,
		slog.New(slog.DiscardHandler),
	)
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
		{
			name:   "empty URL item",
			rawURL: "https://example.com/first,,https://example.com/second",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// When
			client, err := alib.NewClient(tt.rawURL, time.Second, 0, slog.New(slog.DiscardHandler))

			// Then
			require.Error(t, err)
			require.Nil(t, client)
		})
	}
}

func Test_NewClient_rejects_non_positive_timeout(t *testing.T) {
	t.Parallel()

	// When
	client, err := alib.NewClient(
		"https://www.alib.ru/tramka.phtml?tnew=7",
		0,
		0,
		slog.New(slog.DiscardHandler),
	)

	// Then
	require.Error(t, err)
	require.Nil(t, client)
}

func Test_NewClient_rejects_negative_request_interval(t *testing.T) {
	t.Parallel()

	// When
	client, err := alib.NewClient(
		"https://www.alib.ru/tramka.phtml?tnew=7",
		time.Second,
		-time.Second,
		slog.New(slog.DiscardHandler),
	)

	// Then
	require.Error(t, err)
	require.Nil(t, client)
}

func Test_NewClient_rejects_nil_logger(t *testing.T) {
	t.Parallel()

	// When
	client, err := alib.NewClient("https://www.alib.ru/tramka.phtml?tnew=7", time.Second, 0, nil)

	// Then
	require.Error(t, err)
	require.Nil(t, client)
}

func Test_NewClient_does_not_expose_credentials_in_validation_errors(t *testing.T) {
	t.Parallel()

	for _, rawURL := range []string{
		"https://user:top-secret@example.com/books",
		"https://example.com/%zz?access_token=top-secret",
	} {
		// When
		client, err := alib.NewClient(rawURL, time.Second, 0, slog.New(slog.DiscardHandler))

		// Then
		require.Error(t, err)
		require.Nil(t, client)
		require.NotContains(t, err.Error(), "top-secret")
	}
}

func Test_Client_fetches_urls_in_order_and_deduplicates_by_buy_url(t *testing.T) {
	t.Parallel()

	// Given
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.URL.RequestURI())
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch request.URL.Path {
		case "/one/first":
			_, err := writer.Write([]byte(
				listingPage("First", "book-1.html", "100 руб.") +
					listingPage("Second", "../shared-book.html", "200 руб."),
			))
			assert.NoError(t, err)
		case "/two/second":
			_, err := writer.Write([]byte(
				listingPage("Duplicate", "../shared-book.html", "999 руб.") +
					listingPage("Third", "book-3.html", "300 руб."),
			))
			assert.NoError(t, err)
		case "/three/third":
			_, err := writer.Write([]byte(listingPage("Fourth", "book-4.html", "400 руб.")))
			assert.NoError(t, err)
		default:
			t.Errorf("unexpected path %q", request.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	rawURLs := strings.Join([]string{
		" " + server.URL + "/one/first?one=1&two=2",
		server.URL + "/two/second?query=a%2Cb ",
		server.URL + "/three/third?last=true",
	}, ", ")
	client, err := alib.NewClient(rawURLs, time.Second, 0, slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	// When
	books, err := client.Fetch(context.Background())

	// Then
	require.NoError(t, err)
	require.Equal(t, []string{
		"/one/first?one=1&two=2",
		"/two/second?query=a%2Cb",
		"/three/third?last=true",
	}, requests)
	require.Equal(t, []alib.Book{
		{Title: "First", Price: "100 руб.", BuyURL: server.URL + "/one/book-1.html"},
		{Title: "Second", Price: "200 руб.", BuyURL: server.URL + "/shared-book.html"},
		{Title: "Third", Price: "300 руб.", BuyURL: server.URL + "/two/book-3.html"},
		{Title: "Fourth", Price: "400 руб.", BuyURL: server.URL + "/three/book-4.html"},
	}, books)
}

func Test_Client_does_not_expose_query_credentials_in_failure(t *testing.T) {
	t.Parallel()

	// Given
	var logs bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)
	client, err := alib.NewClient(
		server.URL+"/failed?access_token=top-secret",
		time.Second,
		0,
		slog.New(slog.NewTextHandler(&logs, nil)),
	)
	require.NoError(t, err)

	// When
	books, err := client.Fetch(context.Background())

	// Then
	require.ErrorIs(t, err, alib.ErrUnexpectedStatus)
	require.Empty(t, books)
	require.NotContains(t, err.Error(), "top-secret")
	require.NotContains(t, logs.String(), "top-secret")
	require.Contains(t, logs.String(), "url="+server.URL+"/failed")
}

func Test_Client_continues_after_page_failures_and_logs_each_failure(t *testing.T) {
	// Given
	var requests []string
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.URL.Path)
		switch request.URL.Path {
		case "/before", "/after":
			writer.WriteHeader(http.StatusBadGateway)
		case "/broken":
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, err := writer.Write([]byte("<html><body>changed</body></html>"))
			assert.NoError(t, err)
		case "/success":
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, err := writer.Write([]byte(listingPage("Success", "/success-book.html", "100 руб.")))
			assert.NoError(t, err)
		}
	}))
	t.Cleanup(server.Close)
	client, err := alib.NewClient(
		strings.Join([]string{
			server.URL + "/before",
			server.URL + "/broken",
			server.URL + "/success",
			server.URL + "/after",
		}, ","),
		time.Second,
		0,
		logger,
	)
	require.NoError(t, err)

	// When
	books, err := client.Fetch(context.Background())

	// Then
	require.NoError(t, err)
	require.Equal(t, []string{"/before", "/broken", "/success", "/after"}, requests)
	require.Equal(t, []alib.Book{{Title: "Success", Price: "100 руб.", BuyURL: server.URL + "/success-book.html"}}, books)
	require.Equal(t, 3, strings.Count(logs.String(), "msg=alib.page_failed"))
	require.Contains(t, logs.String(), "index=0")
	require.Contains(t, logs.String(), "index=1")
	require.Contains(t, logs.String(), "index=3")
	require.Contains(t, logs.String(), "url="+server.URL+"/before")
	require.Contains(t, logs.String(), "url="+server.URL+"/broken")
	require.Contains(t, logs.String(), "url="+server.URL+"/after")
}

func Test_Client_accepts_all_correct_empty_pages(t *testing.T) {
	t.Parallel()

	// Given
	emptyPage, err := os.ReadFile("testdata/empty.html")
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=windows-1251")
		_, writeErr := writer.Write(emptyPage)
		assert.NoError(t, writeErr)
	}))
	t.Cleanup(server.Close)
	client, err := alib.NewClient(
		server.URL+"/first,"+server.URL+"/second",
		time.Second,
		0,
		slog.New(slog.DiscardHandler),
	)
	require.NoError(t, err)

	// When
	books, err := client.Fetch(context.Background())

	// Then
	require.NoError(t, err)
	require.Empty(t, books)
}

func Test_Client_returns_combined_error_when_all_pages_fail(t *testing.T) {
	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/status" {
			writer.WriteHeader(http.StatusBadGateway)
			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err := writer.Write([]byte("<html><body>changed</body></html>"))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	client, err := alib.NewClient(
		server.URL+"/status,"+server.URL+"/broken",
		time.Second,
		0,
		slog.New(slog.DiscardHandler),
	)
	require.NoError(t, err)

	// When
	books, err := client.Fetch(context.Background())

	// Then
	require.ErrorIs(t, err, alib.ErrUnexpectedStatus)
	require.ErrorIs(t, err, alib.ErrNoBooks)
	require.Empty(t, books)
}

func Test_Client_waits_between_requests(t *testing.T) {
	t.Parallel()

	// Given
	requestTimes := make(chan time.Time, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requestTimes <- time.Now()
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err := writer.Write([]byte(listingPage("Book", "/book.html", "100 руб.")))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	client, err := alib.NewClient(
		server.URL+"/first,"+server.URL+"/second",
		time.Second,
		40*time.Millisecond,
		slog.New(slog.DiscardHandler),
	)
	require.NoError(t, err)

	// When
	_, err = client.Fetch(context.Background())

	// Then
	require.NoError(t, err)
	firstRequest := <-requestTimes
	secondRequest := <-requestTimes
	require.GreaterOrEqual(t, secondRequest.Sub(firstRequest), 30*time.Millisecond)
}

func Test_Client_waits_between_attempts_after_failure(t *testing.T) {
	t.Parallel()

	// Given
	requestTimes := make(chan time.Time, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestTimes <- time.Now()
		if request.URL.Path == "/first" {
			writer.WriteHeader(http.StatusBadGateway)
			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err := writer.Write([]byte(listingPage("Book", "/book.html", "100 руб.")))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	client, err := alib.NewClient(
		server.URL+"/first,"+server.URL+"/second",
		time.Second,
		40*time.Millisecond,
		slog.New(slog.DiscardHandler),
	)
	require.NoError(t, err)

	// When
	_, err = client.Fetch(context.Background())

	// Then
	require.NoError(t, err)
	firstRequest := <-requestTimes
	secondRequest := <-requestTimes
	require.GreaterOrEqual(t, secondRequest.Sub(firstRequest), 30*time.Millisecond)
}

func Test_Client_does_not_wait_after_single_request(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err := writer.Write([]byte(listingPage("Book", "/book.html", "100 руб.")))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	client, err := alib.NewClient(server.URL, time.Second, 5*time.Second, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	result := make(chan error, 1)

	// When
	go func() {
		_, fetchErr := client.Fetch(context.Background())
		result <- fetchErr
	}()

	// Then
	select {
	case err = <-result:
		require.NoError(t, err)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("single request waited for the configured interval")
	}
}

func Test_Client_returns_context_error_when_canceled_during_wait(t *testing.T) {
	// Given
	firstResponse := make(chan struct{})
	allowFirstResponse := make(chan struct{})
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requestCount++
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err := writer.Write([]byte(listingPage("Book", "/book.html", "100 руб.")))
		assert.NoError(t, err)
		if requestCount == 1 {
			close(firstResponse)
			<-allowFirstResponse
		}
	}))
	t.Cleanup(server.Close)
	client, err := alib.NewClient(
		server.URL+"/first,"+server.URL+"/second",
		time.Second,
		5*time.Second,
		slog.New(slog.DiscardHandler),
	)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, fetchErr := client.Fetch(ctx)
		result <- fetchErr
	}()
	<-firstResponse
	close(allowFirstResponse)
	time.Sleep(10 * time.Millisecond)
	cancel()

	// When
	select {
	case err = <-result:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("fetch did not stop promptly after context cancellation")
	}

	// Then
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, requestCount)
}

func listingPage(title, buyURL, price string) string {
	return "<p><b>" + title + "</b> Цена: " + price + " <a href=\"" + buyURL + "\"><b>Купить</b></a></p>"
}
