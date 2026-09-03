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

	prepared, err := client.Process(context.Background(), book)
	require.NoError(t, err)
	require.Equal(t, "http://photo.test/redirect", prepared.Book.Photos[0].URL)
	require.Equal(t, prepared.Book.Photos[0].SlinkURL, prepared.Book.Photos[1].SlinkURL)
	require.Equal(t, prepared.Book.Photos[0].SlinkProfile, prepared.Book.Photos[1].SlinkProfile)
	require.EqualValues(t, 1, downloads.Load())
	require.EqualValues(t, 1, uploads.Load())
	require.NoError(t, prepared.Cleanup())
}

func TestProcess_marksNonImageAndLeavesFailedPhotosUnprocessed(t *testing.T) {
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
	prepared, err := client.Process(context.Background(), alib.Book{Photos: []alib.Photo{
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

func TestProcess_leavesMalformedAndCyclicMetaAndOversizedFilesUnprocessed(t *testing.T) {
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
	prepared, err := client.Process(context.Background(), alib.Book{Photos: []alib.Photo{
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

func TestProcess_rejectsSSRFAndSlinkResponseErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/photo" {
			writeBytes(t, writer, []byte("\xff\xd8\xff\xe0"))
			return
		}
		writer.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	client := testClient(t, server)
	prepared, err := client.Process(context.Background(), alib.Book{Photos: []alib.Photo{
		{URL: "http://127.0.0.1/photo"},
		{URL: "http://photo.test/photo"},
	}})
	require.NoError(t, err)
	require.Empty(t, prepared.Book.Photos[0].SlinkURL)
	require.Empty(t, prepared.Book.Photos[1].SlinkURL)
	require.NoError(t, prepared.Cleanup())
}

func TestProcess_rejectsOversizedAndNonHTTPSlinkResponses(t *testing.T) {
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
	prepared, err := client.Process(context.Background(), alib.Book{Photos: []alib.Photo{
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

func TestProcess_returnsContextCancellation(t *testing.T) {
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
	_, err = client.Process(ctx, alib.Book{Photos: []alib.Photo{{URL: "https://photo.example/image"}}})
	require.ErrorIs(t, err, context.Canceled)
}

func TestProcess_reusesPersistedCurrentProfileWithoutDownloading(t *testing.T) {
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		downloads.Add(1)
		writeText(t, writer, "bad")
	}))
	defer server.Close()
	client := testClient(t, server)
	prepared, err := client.Process(context.Background(), alib.Book{Photos: []alib.Photo{
		{URL: "http://photo.test/photo", Caption: "fresh", SlinkURL: "https://slink.example/image", SlinkProfile: client.Profile()},
	}})
	require.NoError(t, err)
	require.Equal(t, "https://slink.example/image", prepared.Book.Photos[0].SlinkURL)
	require.EqualValues(t, 0, downloads.Load())
	require.NoError(t, prepared.Cleanup())
}

func TestProcess_rejectsRestrictedSourceAddressesBeforeDial(t *testing.T) {
	testCases := []struct {
		name     string
		rawURL   string
		resolved net.IP
	}{
		{name: "literal loopback", rawURL: "http://127.0.0.1/photo"},
		{name: "literal private", rawURL: "http://10.0.0.1/photo"},
		{name: "literal link local", rawURL: "http://169.254.169.254/photo"},
		{name: "literal shared", rawURL: "http://100.100.100.200/photo"},
		{name: "literal documentation", rawURL: "http://203.0.113.10/photo"},
		{name: "literal benchmarking", rawURL: "http://198.18.0.1/photo"},
		{name: "literal reserved", rawURL: "http://240.0.0.1/photo"},
		{name: "literal IPv6 documentation", rawURL: "http://[2001:db8::1]/photo"},
		{name: "literal IPv6 translation", rawURL: "http://[64:ff9b::a9fe:a9fe]/photo"},
		{name: "literal multicast", rawURL: "http://224.0.0.1/photo"},
		{name: "literal unspecified", rawURL: "http://0.0.0.0/photo"},
		{name: "DNS loopback", rawURL: "http://photo.test/photo", resolved: net.ParseIP("127.0.0.1")},
		{name: "DNS private", rawURL: "http://photo.test/photo", resolved: net.ParseIP("192.168.1.1")},
		{name: "DNS link local", rawURL: "http://photo.test/photo", resolved: net.ParseIP("169.254.1.1")},
		{name: "DNS shared", rawURL: "http://photo.test/photo", resolved: net.ParseIP("100.100.100.200")},
		{name: "DNS documentation", rawURL: "http://photo.test/photo", resolved: net.ParseIP("203.0.113.10")},
		{name: "DNS multicast", rawURL: "http://photo.test/photo", resolved: net.ParseIP("224.0.0.1")},
		{name: "DNS unspecified", rawURL: "http://photo.test/photo", resolved: net.ParseIP("0.0.0.0")},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var dialCount atomic.Int32
			client, err := NewClientWithOptions(
				"https://slink.example",
				"key",
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

			require.NoError(t, err)
			require.Zero(t, dialCount.Load())
			require.Empty(t, prepared.Book.Photos[0].SlinkURL)
			require.NoError(t, prepared.Cleanup())
		})
	}
}

func TestProcess_rejectsRestrictedHTTPAndMetaRedirects(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount.Add(1)
		if request.URL.Path == "/http" {
			http.Redirect(writer, request, "http://127.0.0.1/private", http.StatusFound)
			return
		}
		writeText(t, writer, `<meta http-equiv="refresh" content="0;url=http://192.168.1.1/private">`)
	}))
	defer server.Close()
	client := testClient(t, server)

	prepared, err := client.Process(context.Background(), alib.Book{Photos: []alib.Photo{
		{URL: "http://photo.test/http"},
		{URL: "http://photo.test/meta"},
	}})

	require.NoError(t, err)
	require.EqualValues(t, 2, requestCount.Load())
	for _, photo := range prepared.Book.Photos {
		require.Empty(t, photo.SlinkURL)
	}
	require.NoError(t, prepared.Cleanup())
}

func TestProcess_dialsTheValidatedAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/photo" {
			writeBytes(t, writer, []byte("GIF89a"))
			return
		}
		writeText(t, writer, `{"url":"/published"}`)
	}))
	defer server.Close()
	var dialedAddress string
	client, err := NewClientWithOptions(
		server.URL,
		"key",
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
					"key",
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
	client, err := NewClientWithOptions(
		"https://slink.example",
		"key",
		"tag",
		50*time.Millisecond,
		slog.New(slog.DiscardHandler),
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

	require.NoError(t, err)
	require.Less(t, time.Since(startedAt), time.Second)
	require.Empty(t, prepared.Book.Photos[0].SlinkURL)
	require.NoError(t, prepared.Cleanup())
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
				writeBytes(t, writer, testCase.response)
			}))
			defer server.Close()
			client := testClient(t, server)

			prepared, err := client.Process(context.Background(), alib.Book{
				Photos: []alib.Photo{{URL: "http://photo.test/photo"}},
			})

			require.NoError(t, err)
			if testCase.wantSlink {
				require.Equal(t, server.URL+"/published", prepared.Book.Photos[0].SlinkURL)
			} else {
				require.Empty(t, prepared.Book.Photos[0].SlinkURL)
			}
			require.NoError(t, prepared.Cleanup())
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

			require.NoError(t, err)
			if testCase.wantImage {
				require.NotEmpty(t, prepared.Book.Photos[0].SlinkURL)
			} else {
				require.Empty(t, prepared.Book.Photos[0].SlinkURL)
			}
			require.NoError(t, prepared.Cleanup())
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
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	target := server.Listener.Addr().String()
	client, err := NewClientWithOptions(
		server.URL+"/base",
		"key",
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
	client, err := NewClient("https://slink.example/", "private-key", "tag-id", time.Second, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.NotEmpty(t, client.Profile())
	require.NotContains(t, client.Profile(), "private-key")
	_, err = NewClient("ftp://slink.example", "key", "tag", time.Second, slog.New(slog.DiscardHandler))
	require.Error(t, err)
}

func testClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	target := server.Listener.Addr().String()
	client, err := NewClientWithOptions(
		server.URL,
		"secret-api-key",
		"tag-id",
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
	return client
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
