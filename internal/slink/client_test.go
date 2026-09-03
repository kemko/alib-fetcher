package slink

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kemko/alib-fetcher/internal/alib"
)

func TestPrepare_uploadsImageWithAuthAndTagAndCleansFiles(t *testing.T) {
	var uploadCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/photo" {
			writer.Header().Set("Content-Type", "text/plain")
			writeBytes(t, writer, []byte("\x89PNG\r\n\x1a\n"))
			return
		}
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "Bearer secret-api-key", request.Header.Get("Authorization"))
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		assert.Equal(t, []string{"tag-id"}, request.MultipartForm.Value["tagIds"])
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
		assert.Equal(t, "\x89PNG\r\n\x1a\n", readAll(t, file))
		uploadCount.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		if encodeErr := json.NewEncoder(writer).Encode(map[string]string{"url": "/published/image"}); encodeErr != nil {
			t.Error(encodeErr)
		}
	}))
	defer server.Close()

	client := testClient(t, server)
	prepared, err := client.Prepare(context.Background(), alib.Book{
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

func TestPrepare_followsHTTPAndMetaRedirectsAndReusesDuplicate(t *testing.T) {
	var downloads atomic.Int32
	var uploads atomic.Int32
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

	prepared, err := client.Prepare(context.Background(), book)
	require.NoError(t, err)
	require.Equal(t, "http://photo.test/redirect", prepared.Book.Photos[0].URL)
	require.Equal(t, prepared.Book.Photos[0].SlinkURL, prepared.Book.Photos[1].SlinkURL)
	require.Equal(t, prepared.Book.Photos[0].SlinkProfile, prepared.Book.Photos[1].SlinkProfile)
	require.EqualValues(t, 1, downloads.Load())
	require.EqualValues(t, 1, uploads.Load())
	require.NoError(t, prepared.Cleanup())
}

func TestPrepare_marksNonImageAndLeavesFailedPhotosUnprocessed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/bad" {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		writer.Header().Set("Content-Type", "application/octet-stream")
		writeText(t, writer, "not an image")
	}))
	defer server.Close()
	client := testClient(t, server)
	prepared, err := client.Prepare(context.Background(), alib.Book{Photos: []alib.Photo{
		{URL: "http://photo.test/file", Caption: "file"},
		{URL: "http://photo.test/bad", Caption: "bad"},
	}})
	require.NoError(t, err)
	require.True(t, prepared.Book.Photos[0].NonImage)
	require.Equal(t, client.Profile(), prepared.Book.Photos[0].SlinkProfile)
	require.Empty(t, prepared.Book.Photos[1].SlinkURL)
	require.Empty(t, prepared.Book.Photos[1].SlinkProfile)
	require.False(t, prepared.Book.Photos[1].NonImage)
	require.NoError(t, prepared.Cleanup())
}

func TestPrepare_leavesMalformedAndCyclicMetaAndOversizedFilesUnprocessed(t *testing.T) {
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
	prepared, err := client.Prepare(context.Background(), alib.Book{Photos: []alib.Photo{
		{URL: "http://photo.test/bad-meta"},
		{URL: "http://photo.test/cycle-a"},
		{URL: "http://photo.test/large"},
	}})
	require.NoError(t, err)
	for _, photo := range prepared.Book.Photos {
		require.Empty(t, photo.SlinkURL)
		require.Empty(t, photo.SlinkProfile)
		require.False(t, photo.NonImage)
	}
	require.NoError(t, prepared.Cleanup())
}

func TestPrepare_rejectsSSRFAndSlinkResponseErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/photo" {
			writeBytes(t, writer, []byte("\xff\xd8\xff\xe0"))
			return
		}
		writer.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	client := testClient(t, server)
	prepared, err := client.Prepare(context.Background(), alib.Book{Photos: []alib.Photo{
		{URL: "http://127.0.0.1/photo"},
		{URL: "http://photo.test/photo"},
	}})
	require.NoError(t, err)
	require.Empty(t, prepared.Book.Photos[0].SlinkURL)
	require.Empty(t, prepared.Book.Photos[1].SlinkURL)
	require.NoError(t, prepared.Cleanup())
}

func TestPrepare_rejectsOversizedAndNonHTTPSlinkResponses(t *testing.T) {
	var uploadCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/photo" {
			writeBytes(t, writer, []byte("GIF89a"))
			return
		}
		if uploadCount.Add(1) == 2 {
			writeText(t, writer, `{"url":"ftp://slink.example/image"}`)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writeText(t, writer, `{"url":"`+strings.Repeat("x", maxUploadResponse)+`"}`)
	}))
	defer server.Close()
	client := testClient(t, server)
	prepared, err := client.Prepare(context.Background(), alib.Book{Photos: []alib.Photo{
		{URL: "http://photo.test/photo"},
		{URL: "http://photo.test/photo?kind=scheme"},
	}})
	require.NoError(t, err)
	for _, photo := range prepared.Book.Photos {
		require.Empty(t, photo.SlinkURL)
		require.Empty(t, photo.SlinkProfile)
		require.False(t, photo.NonImage)
	}
	require.NoError(t, prepared.Cleanup())
}

func TestPrepare_returnsContextCancellation(t *testing.T) {
	client, err := NewClientWithOptions(
		"https://slink.example",
		"key",
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
	_, err = client.Prepare(ctx, alib.Book{Photos: []alib.Photo{{URL: "https://photo.example/image"}}})
	require.ErrorIs(t, err, context.Canceled)
}

func TestPrepare_reusesPersistedCurrentProfileWithoutDownloading(t *testing.T) {
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		downloads.Add(1)
		writeText(t, writer, "bad")
	}))
	defer server.Close()
	client := testClient(t, server)
	prepared, err := client.Prepare(context.Background(), alib.Book{Photos: []alib.Photo{
		{URL: "http://photo.test/photo", Caption: "fresh", SlinkURL: "https://slink.example/image", SlinkProfile: client.Profile()},
	}})
	require.NoError(t, err)
	require.Equal(t, "https://slink.example/image", prepared.Book.Photos[0].SlinkURL)
	require.EqualValues(t, 0, downloads.Load())
	require.NoError(t, prepared.Cleanup())
}

func TestNewClient_profileDoesNotContainAPIKey(t *testing.T) {
	client, err := NewClient("https://slink.example/", "private-key", "tag-id", time.Second, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.NotEmpty(t, client.Profile())
	require.NotContains(t, client.Profile(), "private-key")
	_, err = NewClient("ftp://slink.example", "key", "tag", time.Second, slog.New(slog.DiscardHandler))
	require.Error(t, err)
}

func testClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := NewClientWithOptions(
		server.URL,
		"secret-api-key",
		"tag-id",
		time.Second,
		slog.New(slog.DiscardHandler),
		Options{
			HTTPClient: &http.Client{Transport: rewriteTransport{target: server.URL}},
			LookupIP: func(context.Context, string) ([]net.IP, error) {
				return []net.IP{net.ParseIP("203.0.113.10")}, nil
			},
		},
	)
	require.NoError(t, err)
	return client
}

type rewriteTransport struct {
	target string
}

func (transport rewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Host != "photo.test" {
		return http.DefaultTransport.RoundTrip(request)
	}
	target, err := url.Parse(transport.target)
	if err != nil {
		return nil, err
	}
	cloned := request.Clone(request.Context())
	cloned.URL.Scheme = target.Scheme
	cloned.URL.Host = target.Host
	response, err := http.DefaultTransport.RoundTrip(cloned)
	if response != nil {
		response.Request = request
	}

	return response, err
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
