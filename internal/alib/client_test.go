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
	books, err := fetchBooks(client, context.Background())

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
	books, err := fetchBooks(client, context.Background())

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
	books, err := fetchBooks(client, context.Background())

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
	books, err := fetchBooks(client, ctx)

	// Then
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, books)
}

func Test_Client_returns_context_error_when_canceled_after_download(t *testing.T) {
	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err := writer.Write([]byte(listingPage("Book", "/book.html", "100 руб.")))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs bytes.Buffer
	logger := slog.New(&cancelOnMessageHandler{
		Handler: slog.NewTextHandler(&logs, nil),
		message: "alib.page_downloaded",
		cancel:  cancel,
	})
	client, err := alib.NewClient(server.URL, time.Second, 0, logger)
	require.NoError(t, err)

	// When
	books, err := fetchBooks(client, ctx)

	// Then
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, books)
	require.Contains(t, logs.String(), "msg=alib.page_downloaded")
	require.NotContains(t, logs.String(), "msg=alib.page_parsed")
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
	books, err := fetchBooks(client, context.Background())

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

func Test_ClientWithResult_deduplicates_failure_after_success_on_later_page(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		if request.URL.Path == "/first" {
			_, err := writer.Write([]byte(`<p><a href="/bs.php4?bs=Seller">BS - Seller</a><br>
<b>Сбойное объявление.</b> М., 2026 г. <a href="/book.html"><b>Купить</b></a></p>`))
			assert.NoError(t, err)
			return
		}
		_, err := writer.Write([]byte(listingPage("Успешное объявление", "/book.html", "100 руб.")))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	client, err := alib.NewClient(server.URL+"/first,"+server.URL+"/second", time.Second, 0, slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	// When
	result, err := client.FetchWithResult(context.Background())

	// Then
	require.NoError(t, err)
	require.Len(t, result.Books, 1)
	require.Empty(t, result.FailedBuyURLs)
}

func Test_ClientWithResult_returns_deduplicated_failed_buy_urls(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		failedListing := `<p><a href="/bs.php4?bs=Seller">BS - Seller</a><br>
<b>Сбойное объявление.</b> М., 2026 г. <a href="/broken.html"><b>Купить</b></a></p>`
		if request.URL.Path == "/first" {
			_, err := writer.Write([]byte(failedListing))
			assert.NoError(t, err)

			return
		}
		_, err := writer.Write([]byte(failedListing + listingPage("Рабочая книга", "/good.html", "100 руб.")))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	client, err := alib.NewClient(server.URL+"/first,"+server.URL+"/second", time.Second, 0, slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	// When
	result, err := client.FetchWithResult(context.Background())

	// Then
	require.NoError(t, err)
	require.Equal(t, []string{server.URL + "/broken.html"}, result.FailedBuyURLs)
	require.Len(t, result.Books, 1)
	require.Equal(t, server.URL+"/good.html", result.Books[0].BuyURL)
}

func Test_Client_logs_full_URL_for_download_failure(t *testing.T) {
	t.Parallel()

	// Given
	var logs bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)
	client, err := alib.NewClient(
		server.URL+"/failed?scope=download",
		time.Second,
		0,
		slog.New(slog.NewTextHandler(&logs, nil)),
	)
	require.NoError(t, err)

	// When
	books, err := fetchBooks(client, context.Background())

	// Then
	require.ErrorIs(t, err, alib.ErrUnexpectedStatus)
	require.Empty(t, books)
	require.Contains(t, err.Error(), "download alib URL \""+server.URL+"/failed?scope=download\"")
	require.Contains(t, logs.String(), "url=\""+server.URL+"/failed?scope=download\"")
}

func Test_Client_logs_full_URL_for_parse_failure(t *testing.T) {
	t.Parallel()

	// Given
	var logs bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err := writer.Write([]byte("<html><body>changed</body></html>"))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	client, err := alib.NewClient(
		server.URL+"/changed?scope=parse#results",
		time.Second,
		0,
		slog.New(slog.NewTextHandler(&logs, nil)),
	)
	require.NoError(t, err)

	// When
	books, err := fetchBooks(client, context.Background())

	// Then
	require.ErrorIs(t, err, alib.ErrNoBooks)
	require.Empty(t, books)
	require.Contains(t, err.Error(), "parse alib URL \""+server.URL+"/changed?scope=parse#results\"")
	logOutput := logs.String()
	require.Contains(t, logOutput, "msg=alib.page_parse_failed index=0 url=\""+
		server.URL+"/changed?scope=parse#results")
}

func Test_Client_does_not_expose_query_credentials_from_malformed_redirect(t *testing.T) {
	t.Parallel()

	// Given
	var logs bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Location", "/next?access_token=top-secret%zz")
		writer.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(server.Close)
	client, err := alib.NewClient(
		server.URL+"/redirect",
		time.Second,
		0,
		slog.New(slog.NewTextHandler(&logs, nil)),
	)
	require.NoError(t, err)

	// When
	books, err := fetchBooks(client, context.Background())

	// Then
	require.Error(t, err)
	require.Empty(t, books)
	require.NotContains(t, err.Error(), "top-secret")
	require.NotContains(t, logs.String(), "top-secret")
	require.Contains(t, logs.String(), "url="+server.URL+"/redirect")
}

func Test_Client_downloads_all_pages_before_parsing_and_logs_outcomes(t *testing.T) {
	// Given
	var requests []string
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	emptyPage, err := os.ReadFile("testdata/empty.html")
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.URL.Path)
		switch request.URL.Path {
		case "/before", "/after":
			writer.WriteHeader(http.StatusBadGateway)
		case "/broken":
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, writeErr := writer.Write([]byte("<html><body>changed</body></html>"))
			assert.NoError(t, writeErr)
		case "/success":
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, writeErr := writer.Write([]byte(listingPage("Success", "/success-book.html", "100 руб.")))
			assert.NoError(t, writeErr)
		case "/empty":
			writer.Header().Set("Content-Type", "text/html; charset=windows-1251")
			_, writeErr := writer.Write(emptyPage)
			assert.NoError(t, writeErr)
		}
	}))
	t.Cleanup(server.Close)
	client, err := alib.NewClient(
		strings.Join([]string{
			server.URL + "/before?access_token=top-secret#fragment-secret",
			server.URL + "/broken?scope=broken",
			server.URL + "/success?scope=success",
			server.URL + "/empty?scope=empty",
			server.URL + "/after?scope=after",
		}, ","),
		time.Second,
		0,
		logger,
	)
	require.NoError(t, err)

	// When
	books, err := fetchBooks(client, context.Background())

	// Then
	require.NoError(t, err)
	require.Equal(t, []string{"/before", "/broken", "/success", "/empty", "/after"}, requests)
	require.Equal(t, []alib.Book{{Title: "Success", Price: "100 руб.", BuyURL: server.URL + "/success-book.html"}}, books)
	logOutput := logs.String()
	require.Equal(t, 3, strings.Count(logOutput, "msg=alib.page_downloaded"))
	require.Equal(t, 2, strings.Count(logOutput, "msg=alib.page_download_failed"))
	require.Equal(t, 1, strings.Count(logOutput, "msg=alib.page_parse_failed"))
	require.Equal(t, 2, strings.Count(logOutput, "msg=alib.page_parsed"))
	require.Contains(t, logOutput, "msg=alib.page_download_failed index=0")
	require.Contains(t, logOutput, "msg=alib.page_download_failed index=0 url=\""+server.URL+
		"/before?access_token=top-secret#fragment-secret\" error=\"alib returned an unexpected status: 502 Bad Gateway\"")
	require.Contains(t, logOutput, "msg=alib.page_parse_failed index=1 url=\""+server.URL+
		"/broken?scope=broken\" error=\"page contains no book listings\"")
	require.Contains(t, logOutput, "msg=alib.page_downloaded index=2 url=\""+server.URL+"/success?scope=success\"")
	require.Contains(t, logOutput, "msg=alib.page_downloaded index=3 url=\""+server.URL+"/empty?scope=empty\"")
	require.Contains(t, logOutput, "msg=alib.page_download_failed index=4 url=\""+server.URL+"/after?scope=after\"")
	require.Contains(t, logOutput, "msg=alib.page_parsed index=2 url=\""+server.URL+"/success?scope=success\"")
	require.Contains(t, logOutput, "msg=alib.page_parsed index=3 url=\""+server.URL+"/empty?scope=empty\" books=0")
	require.Less(t,
		strings.Index(logOutput, "msg=alib.page_download_failed index=4"),
		strings.Index(logOutput, "msg=alib.page_parse_failed index=1"),
	)
}

func Test_Client_rejects_oversized_response_without_content_length(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		flusher, ok := writer.(http.Flusher)
		if !assert.True(t, ok) {
			return
		}
		flusher.Flush()
		_, err := writer.Write(bytes.Repeat([]byte("x"), 4<<20+1))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	client, err := alib.NewClient(server.URL, 5*time.Second, 0, slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	// When
	books, err := fetchBooks(client, context.Background())

	// Then
	require.ErrorContains(t, err, "alib response exceeds 4194304 bytes")
	require.Empty(t, books)
}

func Test_Client_continues_after_response_body_read_failure(t *testing.T) {
	t.Parallel()

	// Given
	var logs bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		if request.URL.Path == "/truncated" {
			writer.Header().Set("Content-Length", "100")
			_, err := writer.Write([]byte("short"))
			assert.NoError(t, err)
			return
		}
		_, err := writer.Write([]byte(listingPage("Book", "/book.html", "100 руб.")))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	client, err := alib.NewClient(
		server.URL+"/truncated,"+server.URL+"/success",
		time.Second,
		0,
		slog.New(slog.NewTextHandler(&logs, nil)),
	)
	require.NoError(t, err)

	// When
	books, err := fetchBooks(client, context.Background())

	// Then
	require.NoError(t, err)
	require.Equal(t, []alib.Book{{Title: "Book", Price: "100 руб.", BuyURL: server.URL + "/book.html"}}, books)
	require.Contains(t, logs.String(), "msg=alib.page_download_failed index=0")
	require.Contains(t, logs.String(), "error=\"read alib response: unexpected EOF\"")
	require.NotContains(t, logs.String(), "msg=alib.page_parsed index=0")
	require.Contains(t, logs.String(), "msg=alib.page_parsed index=1")
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
	books, err := fetchBooks(client, context.Background())

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
		server.URL+"/status?status=bad,"+server.URL+"/broken?scope=broken",
		time.Second,
		0,
		slog.New(slog.DiscardHandler),
	)
	require.NoError(t, err)

	// When
	books, err := fetchBooks(client, context.Background())

	// Then
	require.ErrorIs(t, err, alib.ErrUnexpectedStatus)
	require.ErrorIs(t, err, alib.ErrNoBooks)
	require.Empty(t, books)
	require.Contains(t, err.Error(), "download alib URL \""+server.URL+"/status?status=bad\"")
	require.Contains(t, err.Error(), "parse alib URL \""+server.URL+"/broken?scope=broken\"")
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
	_, err = client.FetchWithResult(context.Background())

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
	_, err = client.FetchWithResult(context.Background())

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
		_, fetchErr := client.FetchWithResult(context.Background())
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
		_, fetchErr := client.FetchWithResult(ctx)
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

func Test_Client_returns_context_error_when_canceled_during_body_download(t *testing.T) {
	// Given
	downloadStarted := make(chan struct{})
	requests := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests <- request.URL.Path
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		flusher, ok := writer.(http.Flusher)
		if !assert.True(t, ok) {
			return
		}
		flusher.Flush()
		close(downloadStarted)
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)
	client, err := alib.NewClient(
		server.URL+"/first,"+server.URL+"/second",
		5*time.Second,
		0,
		slog.New(slog.DiscardHandler),
	)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, fetchErr := client.FetchWithResult(ctx)
		result <- fetchErr
	}()
	<-downloadStarted
	cancel()

	// When
	select {
	case err = <-result:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("fetch did not stop promptly after body-download cancellation")
	}

	// Then
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, "/first", <-requests)
	select {
	case path := <-requests:
		t.Fatalf("unexpected request after cancellation: %s", path)
	default:
	}
}

func listingPage(title, buyURL, price string) string {
	return "<p><b>" + title + "</b> Цена: " + price + " <a href=\"" + buyURL + "\"><b>Купить</b></a></p>"
}

func fetchBooks(client *alib.Client, ctx context.Context) ([]alib.Book, error) {
	result, err := client.FetchWithResult(ctx)

	return result.Books, err
}

type cancelOnMessageHandler struct {
	slog.Handler
	cancel  context.CancelFunc
	message string
}

func (handler *cancelOnMessageHandler) Handle(ctx context.Context, record slog.Record) error {
	err := handler.Handler.Handle(ctx, record)
	if record.Message == handler.message {
		handler.cancel()
	}

	return err
}
