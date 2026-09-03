package slink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kemko/alib-fetcher/internal/alib"
)

func TestProcess_uploadsImageWithAuthAndTagAndCleansFiles(t *testing.T) {
	var uploadCount atomic.Int32
	var origin string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/photo" {
			assert.Equal(t, userAgent, request.Header.Get("User-Agent"))
			assert.Equal(t, "https://alib.example/book", request.Header.Get("Referer"))
			writer.Header().Set("Content-Type", "text/plain")
			writeBytes(t, writer, []byte("\x89PNG\r\n\x1a\n"))
			return
		}
		if request.Method == http.MethodHead {
			writer.Header().Set("Content-Type", "image/png")
			return
		}
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "/api/external/upload", request.URL.Path)
		assert.Equal(t, "Bearer sk_secret-api-key", request.Header.Get("Authorization"))
		assert.Equal(t, origin, request.Header.Get("Origin"))
		assert.Equal(t, userAgent, request.Header.Get("User-Agent"))
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		assert.Equal(t, []string{"tag-id"}, request.MultipartForm.Value["tagIds[]"])
		assert.NotContains(t, request.MultipartForm.Value, "tagIds")
		file, header, err := request.FormFile("image")
		if err != nil {
			t.Error(err)
			return
		}
		defer func() {
			if closeErr := file.Close(); closeErr != nil {
				t.Error(closeErr)
			}
		}()
		assert.Equal(t, "image.png", header.Filename)
		assert.Equal(t, "image/png", header.Header.Get("Content-Type"))
		assert.Equal(t, "\x89PNG\r\n\x1a\n", readAll(t, file))
		uploadCount.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		if encodeErr := json.NewEncoder(writer).Encode(map[string]string{"url": "/published/image"}); encodeErr != nil {
			t.Error(encodeErr)
		}
	}))
	defer server.Close()
	origin = server.URL

	client := testClient(t, server)
	prepared, err := client.Process(context.Background(), alib.Book{
		BuyURL: "https://alib.example/book",
		Photos: []alib.Photo{{URL: "http://photo.test/photo", Caption: "Обложка"}},
	})
	require.NoError(t, err)
	require.Equal(t, server.URL+"/published/image", prepared.Book.Photos[0].SlinkURL)
	require.Equal(t, client.Profile(), prepared.Book.Photos[0].SlinkProfile)
	require.False(t, prepared.Book.Photos[0].NonImage)
	require.EqualValues(t, 1, uploadCount.Load())
	entries, err := os.ReadDir(prepared.TemporaryDirectory())
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	require.NoError(t, prepared.Cleanup())
	require.NoError(t, prepared.Cleanup())
	_, err = os.Stat(prepared.TemporaryDirectory())
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestProcess_followsHTTPAndMetaRedirectsAndReusesDuplicate(t *testing.T) {
	var downloads atomic.Int32
	var uploads atomic.Int32
	var mediaChecks atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/redirect":
			http.Redirect(writer, request, "http://photo.test/meta", http.StatusFound)
		case "/meta":
			writer.Header().Set("Content-Type", "text/html")
			writeText(t, writer, `<META HTTP-EQUIV="Refresh" CONTENT="0; URL=/image">`)
		case "/image":
			downloads.Add(1)
			writeBytes(t, writer, []byte("GIF89a"))
		case "/uploaded":
			mediaChecks.Add(1)
			writer.Header().Set("Content-Type", "image/gif")
		default:
			uploads.Add(1)
			writer.Header().Set("Content-Type", "application/json")
			writeText(t, writer, `{"url":"/uploaded"}`)
		}
	}))
	defer server.Close()
	client := testClient(t, server)
	book := alib.Book{Photos: []alib.Photo{
		{URL: "http://photo.test/redirect", Caption: "first"},
		{URL: "http://photo.test/redirect", Caption: "second"},
	}}

	prepared, err := client.Process(context.Background(), book)
	require.NoError(t, err)
	require.Equal(t, "http://photo.test/redirect", prepared.Book.Photos[0].URL)
	require.Equal(t, prepared.Book.Photos[0].SlinkURL, prepared.Book.Photos[1].SlinkURL)
	require.Equal(t, prepared.Book.Photos[0].SlinkProfile, prepared.Book.Photos[1].SlinkProfile)
	require.EqualValues(t, 1, downloads.Load())
	require.EqualValues(t, 1, uploads.Load())
	require.EqualValues(t, 1, mediaChecks.Load())
	require.NoError(t, prepared.Cleanup())
}

func TestProcess_logsPhotoLifecycleOutcomes(t *testing.T) {
	var logs bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/private-image":
			writeBytes(t, writer, []byte("GIF89a"))
		case "/private-document":
			writer.Header().Set("Content-Type", "text/plain")
			writeText(t, writer, "not an image")
		case "/api/external/upload":
			writer.Header().Set("Content-Type", "application/json")
			writeText(t, writer, `{"url":"/uploaded"}`)
		case "/uploaded":
			assert.Equal(t, http.MethodHead, request.Method)
			writer.Header().Set("Content-Type", "image/gif")
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewClientWithOptions(
		server.URL,
		"sk_lifecycle-secret",
		"tag-id",
		time.Second,
		slog.New(slog.NewJSONHandler(&logs, nil)),
		testOptions(server),
	)
	require.NoError(t, err)
	const (
		buyURL   = "https://alib.example/private-book"
		imageURL = "http://photo.test/private-image"
	)
	prepared, err := client.Process(context.Background(), alib.Book{
		BuyURL: buyURL,
		Photos: []alib.Photo{
			{URL: imageURL},
			{URL: imageURL},
			{URL: "http://photo.test/private-document"},
		},
	})
	require.NoError(t, err)
	temporaryDirectory := prepared.TemporaryDirectory()
	require.NoError(t, prepared.Cleanup())

	logOutput := logs.String()
	require.Equal(t, 3, strings.Count(logOutput, `"msg":"slink.photo_started"`))
	require.Equal(t, 3, strings.Count(logOutput, `"msg":"slink.photo_completed"`))
	require.Contains(t, logOutput, `"msg":"slink.photo_started","buy_url":"`+buyURL+`","index":0,"total":3`)
	require.Contains(t, logOutput, `"msg":"slink.photo_completed","buy_url":"`+buyURL+`","index":0,"total":3,"outcome":"uploaded","media_url":"`+server.URL+`/uploaded"`)
	require.Contains(t, logOutput, `"msg":"slink.photo_completed","buy_url":"`+buyURL+`","index":1,"total":3,"outcome":"duplicate","media_url":"`+server.URL+`/uploaded"`)
	require.Contains(t, logOutput, `"msg":"slink.photo_completed","buy_url":"`+buyURL+`","index":2,"total":3,"outcome":"source_link"`)
	require.NotContains(t, logOutput, imageURL)
	require.NotContains(t, logOutput, temporaryDirectory)
	require.NotContains(t, logOutput, "sk_lifecycle-secret")
}

func TestProcess_logsPersistedReuse(t *testing.T) {
	var logs bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/i/old-code":
			http.Redirect(writer, request, "/image/direct.png", http.StatusFound)
		case "/image/direct.png":
			assert.Equal(t, http.MethodHead, request.Method)
			writer.Header().Set("Content-Type", "image/png")
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewClientWithOptions(
		server.URL,
		"sk_reuse-secret",
		"tag-id",
		time.Second,
		slog.New(slog.NewJSONHandler(&logs, nil)),
		testOptions(server),
	)
	require.NoError(t, err)
	const buyURL = "https://alib.example/reused-book"
	prepared, err := client.Process(context.Background(), alib.Book{
		BuyURL: buyURL,
		Photos: []alib.Photo{{
			URL:          "http://photo.test/private-source",
			SlinkURL:     server.URL + "/i/old-code",
			SlinkProfile: client.Profile(),
		}},
	})
	require.NoError(t, err)
	require.Equal(t, server.URL+"/image/direct.png", prepared.Book.Photos[0].SlinkURL)
	require.NoError(t, prepared.Cleanup())

	logOutput := logs.String()
	require.Contains(t, logOutput, `"msg":"slink.photo_started","buy_url":"`+buyURL+`","index":0,"total":1`)
	require.Contains(t, logOutput, `"msg":"slink.photo_completed","buy_url":"`+buyURL+`","index":0,"total":1,"outcome":"reused","media_url":"`+server.URL+`/image/direct.png"`)
	require.NotContains(t, logOutput, "private-source")
	require.NotContains(t, logOutput, "sk_reuse-secret")
}

func TestProcess_logsMediaResolutionFailure(t *testing.T) {
	var logs bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/private-photo":
			writeBytes(t, writer, []byte("GIF89a"))
		case "/api/external/upload":
			writer.Header().Set("Content-Type", "application/json")
			writeText(t, writer, `{"url":"/i/failed"}`)
		case "/i/failed":
			writer.WriteHeader(http.StatusBadGateway)
			writeText(t, writer, "private response body")
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewClientWithOptions(
		server.URL,
		"sk_media-secret",
		"tag-id",
		time.Second,
		slog.New(slog.NewJSONHandler(&logs, nil)),
		testOptions(server),
	)
	require.NoError(t, err)
	const (
		buyURL    = "https://alib.example/failed-book"
		sourceURL = "http://photo.test/private-photo"
	)
	prepared, err := client.Process(context.Background(), alib.Book{
		BuyURL: buyURL,
		Photos: []alib.Photo{{URL: sourceURL}},
	})
	require.Error(t, err)
	require.Nil(t, prepared)

	logOutput := logs.String()
	require.Contains(t, logOutput, `"msg":"slink.photo_started","buy_url":"`+buyURL+`","index":0,"total":1`)
	require.Contains(t, logOutput, `"msg":"slink.photo_failed","buy_url":"`+buyURL+`","index":0,"total":1`)
	require.Contains(t, logOutput, `"stage":"slink_media"`)
	require.Contains(t, logOutput, `"error_category":"slink_http"`)
	require.Contains(t, logOutput, `"http_status":502`)
	require.NotContains(t, logOutput, sourceURL)
	require.NotContains(t, logOutput, "private response body")
	require.NotContains(t, logOutput, "sk_media-secret")
}

func TestProcess_resolvesSlinkShareURLToDirectMediaURL(t *testing.T) {
	var mediaChecks atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/photo":
			writeBytes(t, writer, []byte("GIF89a"))
		case "/api/external/upload":
			writer.Header().Set("Content-Type", "application/json")
			writeText(t, writer, `{"url":"i/share-code"}`)
		case "/i/share-code":
			assert.Equal(t, http.MethodHead, request.Method)
			assert.Equal(t, userAgent, request.Header.Get("User-Agent"))
			mediaChecks.Add(1)
			http.Redirect(writer, request, "/image/123.png", http.StatusFound)
		case "/image/123.png":
			assert.Equal(t, http.MethodHead, request.Method)
			mediaChecks.Add(1)
			writer.Header().Set("Content-Type", "image/png")
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := testClient(t, server)

	prepared, err := client.Process(context.Background(), alib.Book{
		Photos: []alib.Photo{{URL: "http://photo.test/photo"}},
	})

	require.NoError(t, err)
	require.Equal(t, server.URL+"/image/123.png", prepared.Book.Photos[0].SlinkURL)
	require.EqualValues(t, 2, mediaChecks.Load())
	require.NoError(t, prepared.Cleanup())
}

func TestProcess_rejectsInvalidSlinkMediaResponses(t *testing.T) {
	offOrigin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "image/png")
	}))
	t.Cleanup(offOrigin.Close)

	testCases := []struct {
		name, contentType, location, wantCategory string
		status, wantStatus                        int
	}{
		{name: "HTML response", status: http.StatusOK, contentType: "text/html", wantCategory: "content_type"},
		{name: "not found", status: http.StatusNotFound, wantCategory: "slink_http", wantStatus: http.StatusNotFound},
		{name: "server error", status: http.StatusBadGateway, wantCategory: "slink_http", wantStatus: http.StatusBadGateway},
		{name: "missing Location", status: http.StatusFound, wantCategory: "slink_http", wantStatus: http.StatusFound},
		{name: "off-origin Location", status: http.StatusFound, location: offOrigin.URL + "/image", wantCategory: "request"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/photo":
					writeBytes(t, writer, []byte("GIF89a"))
				case "/api/external/upload":
					writer.Header().Set("Content-Type", "application/json")
					writeText(t, writer, `{"url":"/i/share-code"}`)
				case "/i/share-code":
					if testCase.location != "" {
						writer.Header().Set("Location", testCase.location)
					}
					if testCase.status != 0 {
						writer.WriteHeader(testCase.status)
					}
					if testCase.contentType != "" {
						writer.Header().Set("Content-Type", testCase.contentType)
					}
				default:
					writer.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)
			client := testClient(t, server)

			prepared, err := client.Process(context.Background(), alib.Book{
				Photos: []alib.Photo{{URL: "http://photo.test/photo"}},
			})

			require.Error(t, err)
			require.Nil(t, prepared)
			details := photoFailureDetailsFromError(err)
			require.Equal(t, "slink_media", details.stage)
			require.Equal(t, testCase.wantCategory, details.category)
			require.Equal(t, testCase.wantStatus, details.status)
		})
	}
}

func TestProcess_honorsCancellationDuringSlinkMediaResolution(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/photo":
			writeBytes(t, writer, []byte("GIF89a"))
		case "/api/external/upload":
			writer.Header().Set("Content-Type", "application/json")
			writeText(t, writer, `{"url":"/i/share-code"}`)
		case "/i/share-code":
			close(started)
			<-request.Context().Done()
		}
	}))
	t.Cleanup(server.Close)
	client := testClient(t, server)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.Process(ctx, alib.Book{Photos: []alib.Photo{{URL: "http://photo.test/photo"}}})
		result <- err
	}()
	<-started
	cancel()

	require.ErrorIs(t, <-result, context.Canceled)
}

func TestProcess_marksNonImageAndPreservesSourceLink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/octet-stream")
		writeText(t, writer, "not an image")
	}))
	defer server.Close()
	client := testClient(t, server)
	const sourceURL = "http://photo.test/file"
	prepared, err := client.Process(context.Background(), alib.Book{Photos: []alib.Photo{{URL: sourceURL, Caption: "file"}}})
	require.NoError(t, err)
	require.True(t, prepared.Book.Photos[0].NonImage)
	require.Equal(t, sourceURL, prepared.Book.Photos[0].URL)
	require.Equal(t, client.Profile(), prepared.Book.Photos[0].SlinkProfile)
	require.NoError(t, prepared.Cleanup())
}

func TestProcess_returnsFirstPhotoErrorAndCleansTemporaryFiles(t *testing.T) {
	temporaryRoot := t.TempDir()
	t.Setenv("TMPDIR", temporaryRoot)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	client, err := NewClientWithOptions(
		server.URL,
		"sk_failure-key",
		"tag-id",
		time.Second,
		logger,
		Options{
			LookupIP: func(context.Context, string) ([]net.IP, error) {
				return []net.IP{net.ParseIP("8.8.8.8")}, nil
			},
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			},
		},
	)
	require.NoError(t, err)

	prepared, err := client.Process(context.Background(), alib.Book{
		BuyURL: "https://alib.example/book",
		Photos: []alib.Photo{{URL: "http://photo.test/photo"}},
	})

	require.Error(t, err)
	require.Nil(t, prepared)
	entries, readErr := os.ReadDir(temporaryRoot)
	require.NoError(t, readErr)
	require.Empty(t, entries)
	require.Contains(t, logs.String(), `"stage":"source_download"`)
	require.Contains(t, logs.String(), `"error_category":"source_http"`)
	require.Contains(t, logs.String(), `"http_status":403`)
	require.NotContains(t, logs.String(), "photo.test")
	require.NotContains(t, logs.String(), "sk_failure-key")
}

func TestProcess_reportsSlinkHTTPFailureWithoutSensitiveDetails(t *testing.T) {
	var logs bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/photo" {
			writeBytes(t, writer, []byte("GIF89a"))
			return
		}
		writer.WriteHeader(http.StatusBadGateway)
		writeText(t, writer, "private response body")
	}))
	defer server.Close()
	client, err := NewClientWithOptions(
		server.URL,
		"sk_upload-key",
		"tag-id",
		time.Second,
		slog.New(slog.NewJSONHandler(&logs, nil)),
		testOptions(server),
	)
	require.NoError(t, err)

	prepared, err := client.Process(context.Background(), alib.Book{
		BuyURL: "https://alib.example/book",
		Photos: []alib.Photo{{URL: "http://photo.test/photo"}},
	})

	require.Error(t, err)
	require.Nil(t, prepared)
	require.Contains(t, logs.String(), `"stage":"slink_upload"`)
	require.Contains(t, logs.String(), `"error_category":"slink_http"`)
	require.Contains(t, logs.String(), `"http_status":502`)
	require.NotContains(t, logs.String(), "private response body")
	require.NotContains(t, logs.String(), "sk_upload-key")
	require.NotContains(t, logs.String(), "photo.test")
}

func TestSafeReferer_rejectsUnsafeURLsAndStripsFragments(t *testing.T) {
	require.Equal(t, "https://alib.example/book?source=latest", safeReferer("https://alib.example/book?source=latest#private"))
	require.Empty(t, safeReferer("https://user:secret@alib.example/book"))
	require.Empty(t, safeReferer("javascript:alert(1)"))
}

func TestProcess_reportsMetaAndDownloadFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/bad-meta":
			writer.Header().Set("Content-Type", "text/html")
			writeText(t, writer, `<meta http-equiv="refresh" content="0">`)
		case "/cycle-a":
			writer.Header().Set("Content-Type", "text/html")
			writeText(t, writer, `<meta http-equiv="refresh" content="0;url=/cycle-b">`)
		case "/cycle-b":
			writer.Header().Set("Content-Type", "text/html")
			writeText(t, writer, `<meta http-equiv="refresh" content="0;url=/cycle-a">`)
		default:
			writeBytes(t, writer, make([]byte, maxDownloadBytes+1))
		}
	}))
	defer server.Close()
	client := testClient(t, server)
	testCases := map[string]struct {
		path     string
		stage    string
		category string
	}{
		"malformed META refresh": {path: "/bad-meta", stage: "source_meta", category: "meta_redirect"},
		"META refresh cycle":     {path: "/cycle-a", stage: "source_meta", category: "redirect_cycle"},
		"oversized source":       {path: "/large", stage: "source_download", category: "read"},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			requireProcessFailure(t, client, "http://photo.test"+testCase.path, testCase.stage, testCase.category)
		})
	}
}

func TestProcess_rejectsRestrictedSourceURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/photo" {
			writeBytes(t, writer, []byte("\xff\xd8\xff\xe0"))
			return
		}
		writer.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	client := testClient(t, server)
	requireProcessFailure(t, client, "http://127.0.0.1/photo", "source_download", "request")
}

func TestProcess_rejectsOversizedAndNonHTTPSlinkResponses(t *testing.T) {
	testCases := map[string]struct {
		response string
		category string
	}{
		"oversized response": {
			response: `{"url":"` + strings.Repeat("x", maxUploadResponse) + `"}`,
			category: "response_too_large",
		},
		"non-HTTP URL": {response: `{"url":"ftp://slink.example/image"}`, category: "response_url"},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/photo" {
					writeBytes(t, writer, []byte("GIF89a"))
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				writeText(t, writer, testCase.response)
			}))
			t.Cleanup(server.Close)
			requireProcessFailure(t, testClient(t, server), "http://photo.test/photo", "slink_upload", testCase.category)
		})
	}
}

func TestProcess_returnsContextCancellation(t *testing.T) {
	client, err := NewClientWithOptions(
		"https://slink.example",
		"sk_key",
		"tag",
		time.Second,
		slog.New(slog.DiscardHandler),
		Options{LookupIP: func(ctx context.Context, _ string) ([]net.IP, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}},
	)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Process(ctx, alib.Book{Photos: []alib.Photo{{URL: "https://photo.example/image"}}})
	require.ErrorIs(t, err, context.Canceled)
}

func TestProcess_reusesPersistedCurrentProfileWithoutDownloading(t *testing.T) {
	var downloads atomic.Int32
	var mediaChecks atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodHead && request.URL.Path == "/i/old-code" {
			mediaChecks.Add(1)
			http.Redirect(writer, request, "/image/direct.png", http.StatusFound)
			return
		}
		if request.Method == http.MethodHead && request.URL.Path == "/image/direct.png" {
			mediaChecks.Add(1)
			writer.Header().Set("Content-Type", "image/png")
			return
		}
		if request.Method == http.MethodHead {
			writer.Header().Set("Content-Type", "image/png")
			return
		}
		downloads.Add(1)
		writeText(t, writer, "bad")
	}))
	defer server.Close()
	client := testClient(t, server)
	prepared, err := client.Process(context.Background(), alib.Book{Photos: []alib.Photo{
		{URL: "http://photo.test/photo", Caption: "fresh", SlinkURL: server.URL + "/i/old-code", SlinkProfile: client.Profile()},
	}})
	require.NoError(t, err)
	require.Equal(t, server.URL+"/image/direct.png", prepared.Book.Photos[0].SlinkURL)
	require.EqualValues(t, 0, downloads.Load())
	require.EqualValues(t, 2, mediaChecks.Load())
	require.NoError(t, prepared.Cleanup())
}

func TestProcess_rejectsIANAReservedSourceAddressesBeforeDial(t *testing.T) {
	testCases := []struct {
		name     string
		rawURL   string
		resolved net.IP
	}{
		{name: "literal IPv4 loopback", rawURL: "http://127.0.0.1/photo"},
		{name: "literal IPv6 documentation", rawURL: "http://[2001:db8::1]/photo"},
		{name: "literal globally reachable IANA address", rawURL: "http://192.0.0.9/photo"},
		{name: "DNS IPv4 loopback", rawURL: "http://photo.test/photo", resolved: net.ParseIP("127.0.0.1")},
		{name: "DNS IPv6 documentation", rawURL: "http://photo.test/photo", resolved: net.ParseIP("2001:db8::1")},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var dialCount atomic.Int32
			client, err := NewClientWithOptions(
				"https://slink.example",
				"sk_key",
				"tag",
				time.Second,
				slog.New(slog.DiscardHandler),
				Options{
					LookupIP: func(context.Context, string) ([]net.IP, error) {
						return []net.IP{testCase.resolved}, nil
					},
					DialContext: func(context.Context, string, string) (net.Conn, error) {
						dialCount.Add(1)
						return nil, errors.New("unexpected dial")
					},
				},
			)
			require.NoError(t, err)

			prepared, err := client.Process(context.Background(), alib.Book{
				Photos: []alib.Photo{{URL: testCase.rawURL}},
			})

			require.Error(t, err)
			require.Nil(t, prepared)
			require.Zero(t, dialCount.Load())
		})
	}
}

func TestProcess_rejectsMixedDNSAddressesBeforeDial(t *testing.T) {
	var dialCount atomic.Int32
	client, err := NewClientWithOptions(
		"https://slink.example",
		"sk_key",
		"tag",
		time.Second,
		slog.New(slog.DiscardHandler),
		Options{
			LookupIP: func(context.Context, string) ([]net.IP, error) {
				return []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("192.0.0.9")}, nil
			},
			DialContext: func(context.Context, string, string) (net.Conn, error) {
				dialCount.Add(1)
				return nil, errors.New("unexpected dial")
			},
		},
	)
	require.NoError(t, err)

	prepared, err := client.Process(context.Background(), alib.Book{
		Photos: []alib.Photo{{URL: "http://photo.test/photo"}},
	})

	require.Error(t, err)
	require.Nil(t, prepared)
	require.Zero(t, dialCount.Load())
}

func TestProcess_rejectsNilAndEmptyDNSResultsBeforeDial(t *testing.T) {
	testCases := map[string][]net.IP{
		"nil result":   nil,
		"nil address":  {nil},
		"empty result": {},
	}
	for name, resolved := range testCases {
		t.Run(name, func(t *testing.T) {
			var dialCount atomic.Int32
			client, err := NewClientWithOptions(
				"https://slink.example",
				"sk_key",
				"tag",
				time.Second,
				slog.New(slog.DiscardHandler),
				Options{
					LookupIP: func(context.Context, string) ([]net.IP, error) {
						return resolved, nil
					},
					DialContext: func(context.Context, string, string) (net.Conn, error) {
						dialCount.Add(1)
						return nil, errors.New("unexpected dial")
					},
				},
			)
			require.NoError(t, err)

			prepared, err := client.Process(context.Background(), alib.Book{
				Photos: []alib.Photo{{URL: "http://photo.test/photo"}},
			})

			require.Error(t, err)
			require.Nil(t, prepared)
			require.Zero(t, dialCount.Load())
		})
	}
}

func TestProcess_acceptsAddressesOutsideIANARegistryBeforeDial(t *testing.T) {
	testCases := []struct {
		name     string
		rawURL   string
		resolved net.IP
	}{
		{name: "literal multicast", rawURL: "http://224.0.0.1/photo"},
		{name: "literal IPv6 outside former public range", rawURL: "http://[4000::1]/photo"},
		{name: "DNS multicast", rawURL: "http://photo.test/photo", resolved: net.ParseIP("224.0.0.1")},
		{name: "DNS IPv6 outside former public range", rawURL: "http://photo.test/photo", resolved: net.ParseIP("4000::1")},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var dialCount atomic.Int32
			client, err := NewClientWithOptions(
				"https://slink.example",
				"sk_key",
				"tag",
				time.Second,
				slog.New(slog.DiscardHandler),
				Options{
					LookupIP: func(context.Context, string) ([]net.IP, error) {
						return []net.IP{testCase.resolved}, nil
					},
					DialContext: func(context.Context, string, string) (net.Conn, error) {
						dialCount.Add(1)
						return nil, errors.New("expected dial failure")
					},
				},
			)
			require.NoError(t, err)

			prepared, err := client.Process(context.Background(), alib.Book{
				Photos: []alib.Photo{{URL: testCase.rawURL}},
			})

			require.Error(t, err)
			require.Nil(t, prepared)
			require.Positive(t, dialCount.Load())
		})
	}
}

func TestProcess_rejectsRestrictedHTTPAndMetaRedirects(t *testing.T) {
	testCases := map[string]struct {
		path     string
		category string
	}{
		"HTTP": {path: "/http", category: "request"},
		"META": {path: "/meta", category: "request"},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			var requestCount atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requestCount.Add(1)
				if request.URL.Path == "/http" {
					http.Redirect(writer, request, "http://127.0.0.1/private", http.StatusFound)
					return
				}
				writeText(t, writer, `<meta http-equiv="refresh" content="0;url=http://192.168.1.1/private">`)
			}))
			t.Cleanup(server.Close)
			requireProcessFailure(
				t,
				testClient(t, server),
				"http://photo.test"+testCase.path,
				"source_download",
				testCase.category,
			)
			require.EqualValues(t, 1, requestCount.Load())
		})
	}
}

func TestProcess_dialsTheValidatedAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/photo" {
			writeBytes(t, writer, []byte("GIF89a"))
			return
		}
		if request.Method == http.MethodHead {
			writer.Header().Set("Content-Type", "image/gif")
			return
		}
		writeText(t, writer, `{"url":"/published"}`)
	}))
	defer server.Close()
	var dialedAddress string
	client, err := NewClientWithOptions(
		server.URL,
		"sk_key",
		"tag",
		time.Second,
		slog.New(slog.DiscardHandler),
		Options{
			LookupIP: func(context.Context, string) ([]net.IP, error) {
				return []net.IP{net.ParseIP("8.8.8.8")}, nil
			},
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				dialedAddress = address
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			},
		},
	)
	require.NoError(t, err)

	prepared, err := client.Process(context.Background(), alib.Book{
		Photos: []alib.Photo{{URL: "http://photo.test/photo"}},
	})

	require.NoError(t, err)
	require.Equal(t, "8.8.8.8:80", dialedAddress)
	require.NotEmpty(t, prepared.Book.Photos[0].SlinkURL)
	require.NoError(t, prepared.Cleanup())
}

func TestProcess_honorsCancellationAndCleansTemporaryFilesDuringNetworkOperations(t *testing.T) {
	testCases := []struct {
		newClient func(*testing.T, chan<- struct{}, <-chan struct{}) *Client
		name      string
	}{
		{
			name: "DNS lookup",
			newClient: func(t *testing.T, started chan<- struct{}, _ <-chan struct{}) *Client {
				client, err := NewClientWithOptions(
					"https://slink.example",
					"sk_key",
					"tag",
					time.Second,
					slog.New(slog.DiscardHandler),
					Options{LookupIP: func(ctx context.Context, _ string) ([]net.IP, error) {
						started <- struct{}{}
						<-ctx.Done()

						return nil, ctx.Err()
					}},
				)
				require.NoError(t, err)

				return client
			},
		},
		{
			name: "download",
			newClient: func(t *testing.T, started chan<- struct{}, release <-chan struct{}) *Client {
				server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
					started <- struct{}{}
					select {
					case <-release:
					case <-request.Context().Done():
					}
				}))
				t.Cleanup(server.Close)

				return testClient(t, server)
			},
		},
		{
			name: "upload",
			newClient: func(t *testing.T, started chan<- struct{}, release <-chan struct{}) *Client {
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					if request.URL.Path == "/photo" {
						writeBytes(t, writer, []byte("GIF89a"))
						return
					}
					started <- struct{}{}
					select {
					case <-release:
					case <-request.Context().Done():
					}
				}))
				t.Cleanup(server.Close)

				return testClient(t, server)
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			temporaryRoot := t.TempDir()
			t.Setenv("TMPDIR", temporaryRoot)
			started := make(chan struct{}, 1)
			release := make(chan struct{})
			client := testCase.newClient(t, started, release)
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() {
				_, err := client.Process(ctx, alib.Book{Photos: []alib.Photo{{URL: "http://photo.test/photo"}}})
				result <- err
			}()
			<-started

			cancel()
			close(release)

			require.ErrorIs(t, <-result, context.Canceled)
			entries, err := os.ReadDir(temporaryRoot)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}

func TestProcess_honorsTimeoutDuringDNSLookup(t *testing.T) {
	var logs bytes.Buffer
	client, err := NewClientWithOptions(
		"https://slink.example",
		"sk_key",
		"tag",
		50*time.Millisecond,
		slog.New(slog.NewJSONHandler(&logs, nil)),
		Options{LookupIP: func(ctx context.Context, _ string) ([]net.IP, error) {
			<-ctx.Done()

			return nil, ctx.Err()
		}},
	)
	require.NoError(t, err)
	startedAt := time.Now()

	prepared, err := client.Process(context.Background(), alib.Book{
		Photos: []alib.Photo{{URL: "http://photo.test/photo"}},
	})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(startedAt), time.Second)
	require.Nil(t, prepared)
	require.Contains(t, logs.String(), `"stage":"source_download"`)
	require.Contains(t, logs.String(), `"error_category":"request"`)
}

func TestSaveDownloadedFile_enforcesDownloadBoundary(t *testing.T) {
	exactDirectory := t.TempDir()
	saved, err := saveDownloadedFile(exactDirectory, bytes.NewReader(make([]byte, maxDownloadBytes)))
	require.NoError(t, err)
	require.Len(t, saved.content, maxDownloadBytes)

	overDirectory := t.TempDir()
	_, err = saveDownloadedFile(overDirectory, bytes.NewReader(make([]byte, maxDownloadBytes+1)))
	require.ErrorContains(t, err, "exceeds")
	entries, readErr := os.ReadDir(overDirectory)
	require.NoError(t, readErr)
	require.Empty(t, entries)
}

func requireProcessFailure(t *testing.T, client *Client, rawURL, stage, category string) {
	t.Helper()
	temporaryRoot := t.TempDir()
	t.Setenv("TMPDIR", temporaryRoot)

	prepared, err := client.Process(context.Background(), alib.Book{
		Photos: []alib.Photo{{URL: rawURL}},
	})

	require.Error(t, err)
	require.Nil(t, prepared)
	details := photoFailureDetailsFromError(err)
	require.Equal(t, stage, details.stage)
	require.Equal(t, category, details.category)
	require.Zero(t, details.status)
	entries, readErr := os.ReadDir(temporaryRoot)
	require.NoError(t, readErr)
	require.Empty(t, entries)
}

func TestProcess_acceptsExactSlinkResponseLimitAndRejectsInvalidURLs(t *testing.T) {
	testCases := []struct {
		name      string
		response  []byte
		wantSlink bool
	}{
		{
			name:      "exact limit",
			response:  paddedJSONResponse(`{"url":"/published"}`, maxUploadResponse),
			wantSlink: true,
		},
		{name: "over limit", response: paddedJSONResponse(`{"url":"/published"}`, maxUploadResponse+1)},
		{name: "missing URL", response: []byte(`{}`)},
		{name: "empty URL", response: []byte(`{"url":" "}`)},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/photo" {
					writeBytes(t, writer, []byte("GIF89a"))
					return
				}
				if request.Method == http.MethodHead {
					writer.Header().Set("Content-Type", "image/gif")
					return
				}
				writeBytes(t, writer, testCase.response)
			}))
			defer server.Close()
			client := testClient(t, server)

			prepared, err := client.Process(context.Background(), alib.Book{
				Photos: []alib.Photo{{URL: "http://photo.test/photo"}},
			})

			if testCase.wantSlink {
				require.NoError(t, err)
				require.Equal(t, server.URL+"/published", prepared.Book.Photos[0].SlinkURL)
				require.NoError(t, prepared.Cleanup())
				return
			}
			require.Error(t, err)
			require.Nil(t, prepared)
		})
	}
}

func TestProcess_enforcesHTTPAndMetaRedirectBoundaries(t *testing.T) {
	testCases := []struct {
		name      string
		kind      string
		redirects int
		wantImage bool
	}{
		{name: "HTTP limit", kind: "http", redirects: maxHTTPRedirects, wantImage: true},
		{name: "HTTP over limit", kind: "http", redirects: maxHTTPRedirects + 1},
		{name: "META limit", kind: "meta", redirects: maxMetaRedirects, wantImage: true},
		{name: "META over limit", kind: "meta", redirects: maxMetaRedirects + 1},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/api/external/upload" {
					writeText(t, writer, `{"url":"/published"}`)
					return
				}
				if request.Method == http.MethodHead {
					writer.Header().Set("Content-Type", "image/gif")
					return
				}
				index, err := strconv.Atoi(strings.TrimPrefix(request.URL.Path, "/"+testCase.kind+"/"))
				if !assert.NoError(t, err) {
					return
				}
				if index == testCase.redirects {
					writeBytes(t, writer, []byte("GIF89a"))
					return
				}
				next := fmt.Sprintf("/%s/%d", testCase.kind, index+1)
				if testCase.kind == "http" {
					http.Redirect(writer, request, next, http.StatusFound)
					return
				}
				writeText(t, writer, `<meta http-equiv="refresh" content="0;url=`+next+`">`)
			}))
			defer server.Close()
			client := testClient(t, server)

			prepared, err := client.Process(context.Background(), alib.Book{
				Photos: []alib.Photo{{URL: "http://photo.test/" + testCase.kind + "/0"}},
			})

			if testCase.wantImage {
				require.NoError(t, err)
				require.NotEmpty(t, prepared.Book.Photos[0].SlinkURL)
				require.NoError(t, prepared.Cleanup())
				return
			}
			require.Error(t, err)
			require.Nil(t, prepared)
		})
	}
}

func TestProcess_usesSlinkBasePathForUploadAndRelativeResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/photo":
			writeBytes(t, writer, []byte("GIF89a"))
		case "/base/api/external/upload":
			writeText(t, writer, `{"url":"published/image"}`)
		case "/base/published/image":
			writer.Header().Set("Content-Type", "image/png")
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	target := server.Listener.Addr().String()
	client, err := NewClientWithOptions(
		server.URL+"/base",
		"sk_key",
		"tag",
		time.Second,
		slog.New(slog.DiscardHandler),
		Options{
			LookupIP: func(context.Context, string) ([]net.IP, error) {
				return []net.IP{net.ParseIP("8.8.8.8")}, nil
			},
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, target)
			},
		},
	)
	require.NoError(t, err)

	prepared, err := client.Process(context.Background(), alib.Book{
		Photos: []alib.Photo{{URL: "http://photo.test/photo"}},
	})

	require.NoError(t, err)
	require.Equal(t, server.URL+"/base/published/image", prepared.Book.Photos[0].SlinkURL)
	require.NoError(t, prepared.Cleanup())
}

func paddedJSONResponse(payload string, size int) []byte {
	return append([]byte(payload), bytes.Repeat([]byte(" "), size-len(payload))...)
}

func TestNewClient_profileDoesNotContainAPIKey(t *testing.T) {
	client, err := NewClient("https://slink.example/", "sk_private-key", "tag-id", time.Second, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.NotEmpty(t, client.Profile())
	require.NotContains(t, client.Profile(), "sk_private-key")
	_, err = NewClient("ftp://slink.example", "sk_key", "tag", time.Second, slog.New(slog.DiscardHandler))
	require.Error(t, err)
	_, err = NewClient("https://slink.example", "private-key", "tag", time.Second, slog.New(slog.DiscardHandler))
	require.ErrorContains(t, err, "API key must start with sk_")
	require.NotContains(t, err.Error(), "private-key")
}

func testClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := NewClientWithOptions(
		server.URL,
		"sk_secret-api-key",
		"tag-id",
		time.Second,
		slog.New(slog.DiscardHandler),
		testOptions(server),
	)
	require.NoError(t, err)
	return client
}

func testOptions(server *httptest.Server) Options {
	target := server.Listener.Addr().String()

	return Options{
		LookupIP: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("8.8.8.8")}, nil
		},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, target)
		},
	}
}

func readAll(t *testing.T, reader io.Reader) string {
	t.Helper()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Error(err)
	}
	return string(data)
}

func writeText(t *testing.T, writer io.Writer, value string) {
	t.Helper()
	if _, err := io.WriteString(writer, value); err != nil {
		t.Error(err)
	}
}

func writeBytes(t *testing.T, writer io.Writer, value []byte) {
	t.Helper()
	if _, err := writer.Write(value); err != nil {
		t.Error(err)
	}
}
