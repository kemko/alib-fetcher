package app_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/app"
	"github.com/kemko/alib-fetcher/internal/slink"
	"github.com/kemko/alib-fetcher/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_Service_savesPreparedBookBeforeCleanupAndRendering(t *testing.T) {
	t.Parallel()

	// Given
	events := make([]string, 0)
	server := photoServer(&events)
	defer server.Close()
	processor := newTrackingPhotoProcessor(t, server, &events)
	book := alib.Book{
		Title:  "Книга",
		BuyURL: "https://example.com/book",
		Photos: []alib.Photo{{URL: "http://photo.test/photo", Caption: "Обложка"}},
	}
	state := &fakeState{
		pending:  []alib.Book{book},
		existing: []bool{true},
		events:   &events,
	}
	photoState := &photoState{fakeState: state}
	photoState.save = func(_ context.Context, prepared alib.Book) error {
		require.NotEmpty(t, processor.prepared)
		_, err := os.Stat(processor.prepared[0].TemporaryDirectory())
		require.NoError(t, err)
		require.Equal(t, processor.prepared[0].Book, prepared)

		return nil
	}
	sender := &fakeSender{
		events: &events,
		afterSend: func() {
			_, err := os.Stat(processor.prepared[0].TemporaryDirectory())
			require.ErrorIs(t, err, os.ErrNotExist)
		},
	}
	service := app.NewService(app.Dependencies{
		Fetcher:        fakeFetcher{books: []alib.Book{book}, events: &events},
		State:          photoState,
		Sender:         sender,
		PhotoProcessor: processor,
		MessageLimit:   4096,
		Now:            time.Now,
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.NoError(t, err)
	require.Equal(t, app.Result{Fetched: 1, Sent: 1}, result)
	require.Len(t, photoState.saved, 1)
	require.Contains(t, sender.messages[0], "<tg-slideshow>")
	require.Equal(t, []string{"fetch", "record", "pending", "prepare", "upload", "save:" + book.BuyURL, "send", "mark:" + book.BuyURL}, events)
}

func Test_Service_persistsPreparedOversizedPendingBookBeforeSkipping(t *testing.T) {
	t.Parallel()

	// Given
	events := make([]string, 0)
	server := photoServer(&events)
	defer server.Close()
	processor := newTrackingPhotoProcessor(t, server, &events)
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	book := alib.Book{
		Title:  strings.Repeat("Очень длинная книга ", 20),
		BuyURL: "https://example.com/book",
		Photos: []alib.Photo{{URL: "http://photo.test/photo", Caption: "Обложка"}},
	}
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"), now)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, state.Close()) })
	_, err = state.RecordDiscovered(context.Background(), []alib.Book{book}, now)
	require.NoError(t, err)
	service := app.NewService(app.Dependencies{
		Fetcher:        fakeFetcher{},
		State:          state,
		Sender:         &fakeSender{},
		PhotoProcessor: processor,
		MessageLimit:   120,
		Now:            func() time.Time { return now },
	})

	// When
	firstResult, firstErr := service.Run(context.Background())
	secondResult, secondErr := service.Run(context.Background())

	// Then
	require.NoError(t, firstErr)
	require.NoError(t, secondErr)
	require.Equal(t, app.Result{Failed: 1}, firstResult)
	require.Equal(t, app.Result{Failed: 1}, secondResult)
	require.Equal(t, []string{"prepare", "upload", "prepare"}, events)
}

func Test_Service_cleansPreparedFilesWhenStateSaveFails(t *testing.T) {
	t.Parallel()

	// Given
	events := make([]string, 0)
	server := photoServer(&events)
	defer server.Close()
	processor := newTrackingPhotoProcessor(t, server, &events)
	book := alib.Book{Title: "Книга", BuyURL: "https://example.com/book", Photos: []alib.Photo{{URL: "http://photo.test/photo"}}}
	stateErr := errors.New("state unavailable")
	state := &photoState{
		fakeState: &fakeState{pending: []alib.Book{book}, existing: []bool{true}},
		save: func(context.Context, alib.Book) error {
			return stateErr
		},
	}
	sender := &fakeSender{}
	service := app.NewService(app.Dependencies{
		Fetcher:        fakeFetcher{books: []alib.Book{book}},
		State:          state,
		Sender:         sender,
		PhotoProcessor: processor,
		MessageLimit:   4096,
		Now:            time.Now,
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.ErrorIs(t, err, stateErr)
	require.Equal(t, app.Result{Fetched: 1}, result)
	require.Empty(t, sender.messages)
	require.Len(t, processor.prepared, 1)
	_, statErr := os.Stat(processor.prepared[0].TemporaryDirectory())
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func Test_Service_cleansPreparedFilesWhenStateSaveIsCanceled(t *testing.T) {
	t.Parallel()

	// Given
	server := photoServer(nil)
	defer server.Close()
	processor := newTrackingPhotoProcessor(t, server, nil)
	book := alib.Book{Title: "Книга", BuyURL: "https://example.com/book", Photos: []alib.Photo{{URL: "http://photo.test/photo"}}}
	ctx, cancel := context.WithCancel(context.Background())
	state := &photoState{
		fakeState: &fakeState{pending: []alib.Book{book}, existing: []bool{true}},
		save: func(context.Context, alib.Book) error {
			cancel()

			return context.Canceled
		},
	}
	sender := &fakeSender{}
	service := app.NewService(app.Dependencies{
		Fetcher:        fakeFetcher{books: []alib.Book{book}},
		State:          state,
		Sender:         sender,
		PhotoProcessor: processor,
		MessageLimit:   4096,
		Now:            time.Now,
	})

	// When
	result, err := service.Run(ctx)

	// Then
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, app.Result{Fetched: 1}, result)
	require.Empty(t, sender.messages)
	require.Len(t, processor.prepared, 1)
	_, statErr := os.Stat(processor.prepared[0].TemporaryDirectory())
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func Test_Service_keepsPhotoPathUnchangedWithoutProcessor(t *testing.T) {
	t.Parallel()

	// Given
	book := alib.Book{
		Title:  "Книга",
		BuyURL: "https://example.com/book",
		Photos: []alib.Photo{{URL: "https://example.com/photo", Caption: "Обложка"}},
	}
	sender := &fakeSender{}
	state := &fakeState{pending: []alib.Book{book}, recordedNew: 1}
	service := app.NewService(app.Dependencies{
		Fetcher:      fakeFetcher{books: []alib.Book{book}},
		State:        state,
		Sender:       sender,
		MessageLimit: 4096,
		Now:          time.Now,
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.NoError(t, err)
	require.Equal(t, app.Result{Fetched: 1, New: 1, Sent: 1}, result)
	require.Contains(t, sender.messages[0], `Смотрите: <a href="https://example.com/photo">Обложка</a>`)
	require.NotContains(t, sender.messages[0], "tg-slideshow")
}

func Test_Service_integratesPhotoPreparationAndSlideshow(t *testing.T) {
	t.Parallel()

	// Given
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/meta":
			writer.Header().Set("Content-Type", "text/html")
			_, err := io.WriteString(writer, `<meta http-equiv="REFRESH" content="0; URL=/image">`)
			assert.NoError(t, err)
		case "/image":
			writer.Header().Set("Content-Type", "text/plain")
			_, err := writer.Write([]byte("\x89PNG\r\n\x1a\n"))
			assert.NoError(t, err)
		case "/document":
			writer.Header().Set("Content-Type", "text/plain")
			_, err := io.WriteString(writer, "not an image")
			assert.NoError(t, err)
		case "/api/external/upload":
			assert.Equal(t, "Bearer sk_integration-api-key", request.Header.Get("Authorization"))
			if err := request.ParseMultipartForm(1 << 20); !assert.NoError(t, err) {
				return
			}
			assert.Equal(t, []string{"integration-tag"}, request.MultipartForm.Value["tagIds[]"])
			writer.Header().Set("Content-Type", "application/json")
			_, err := io.WriteString(writer, `{"url":"/i/share-code"}`)
			assert.NoError(t, err)
		case "/i/share-code":
			assert.Equal(t, http.MethodHead, request.Method)
			http.Redirect(writer, request, "/published/image", http.StatusFound)
		case "/published/image":
			assert.Equal(t, http.MethodHead, request.Method)
			writer.Header().Set("Content-Type", "image/png")
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, err := slink.NewClientWithOptions(
		server.URL,
		"sk_integration-api-key",
		"integration-tag",
		time.Second,
		logger,
		slink.Options{
			LookupIP: func(context.Context, string) ([]net.IP, error) {
				return []net.IP{net.ParseIP("93.184.216.34")}, nil
			},
			DialContext: serverDialContext(server),
		},
	)
	require.NoError(t, err)
	book := alib.Book{
		Title:  "Книга",
		BuyURL: "https://example.com/book",
		Photos: []alib.Photo{
			{URL: "http://photo.test/meta", Caption: "Обложка"},
			{URL: "http://photo.test/document", Caption: "Документ"},
		},
	}
	state := &photoState{fakeState: &fakeState{pending: []alib.Book{book}, recordedNew: 1}}
	processor := client
	sender := &fakeSender{}
	service := app.NewService(app.Dependencies{
		Fetcher:        fakeFetcher{books: []alib.Book{book}},
		State:          state,
		Sender:         sender,
		PhotoProcessor: processor,
		MessageLimit:   4096,
		Now:            time.Now,
	})

	// When
	result, err := service.Run(context.Background())

	// Then
	require.NoError(t, err)
	require.Equal(t, app.Result{Fetched: 1, New: 1, Sent: 1}, result)
	require.Empty(t, state.saved)
	require.Contains(t, sender.messages[0], `<tg-slideshow><img src="`+server.URL+`/published/image"/><figcaption>Обложка</figcaption></tg-slideshow>`)
	require.Contains(t, sender.messages[0], `Смотрите: <a href="http://photo.test/document">Документ</a>`)
	require.NotContains(t, sender.messages[0], "http://photo.test/meta")
	require.NotContains(t, logs.String(), "sk_integration-api-key")
}

func Test_Service_integratesParsedPhotosWithPersistentStore(t *testing.T) {
	// Given
	temporaryRoot := t.TempDir()
	t.Setenv("TMPDIR", temporaryRoot)
	var uploads atomic.Int32
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/foto.php4" && request.URL.Query().Get("kind") == "meta":
			writer.Header().Set("Content-Type", "text/html")
			_, err := io.WriteString(writer, `<meta http-equiv="refresh" content="0;url=/image">`)
			assert.NoError(t, err)
		case request.URL.Path == "/foto.php4" && request.URL.Query().Get("kind") == "document":
			_, err := io.WriteString(writer, "not an image")
			assert.NoError(t, err)
		case request.URL.Path == "/foto.php4" && request.URL.Query().Get("kind") == "failure":
			writer.WriteHeader(http.StatusBadGateway)
		case request.URL.Path == "/image":
			_, err := writer.Write([]byte("\x89PNG\r\n\x1a\n"))
			assert.NoError(t, err)
		case request.URL.Path == "/base/api/external/upload":
			uploads.Add(1)
			assert.Equal(t, "Bearer sk_persistent-api-key", request.Header.Get("Authorization"))
			file, header, err := request.FormFile("image")
			if !assert.NoError(t, err) {
				return
			}
			assert.Equal(t, "image/png", header.Header.Get("Content-Type"))
			assert.NoError(t, file.Close())
			assert.Equal(t, []string{"integration-tag"}, request.MultipartForm.Value["tagIds[]"])
			_, err = io.WriteString(writer, `{"url":"i/share-code"}`)
			assert.NoError(t, err)
		case request.URL.Path == "/base/i/share-code":
			assert.Equal(t, http.MethodHead, request.Method)
			http.Redirect(writer, request, "/base/published/image", http.StatusFound)
		case request.URL.Path == "/base/published/image":
			assert.Equal(t, http.MethodHead, request.Method)
			writer.Header().Set("Content-Type", "image/png")
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, err := slink.NewClientWithOptions(
		server.URL+"/base",
		"sk_persistent-api-key",
		"integration-tag",
		time.Second,
		logger,
		slink.Options{
			LookupIP: func(context.Context, string) ([]net.IP, error) {
				return []net.IP{net.ParseIP("93.184.216.34")}, nil
			},
			DialContext: serverDialContext(server),
		},
	)
	require.NoError(t, err)
	pageURL, err := url.Parse("http://photo.test/list")
	require.NoError(t, err)
	books, err := alib.Parse(strings.NewReader(`<p><b>Книга.</b> М., 2026 г.<br>
Цена: 100 руб. <a href="/book"><b>Купить</b></a><br>
Смотрите: <a href="/foto.php4?kind=meta">Обложка</a> -
<a href="/foto.php4?kind=document">Документ</a></p>`), pageURL, "text/html")
	require.NoError(t, err)
	require.Len(t, books, 1)
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	statePath := filepath.Join(t.TempDir(), "state.db")
	firstState, err := store.Open(statePath, now)
	require.NoError(t, err)
	deliveryErr := errors.New("telegram unavailable")
	firstSender := &fakeSender{err: deliveryErr, failAt: 1, afterSend: func() {
		requireTemporaryRootEmpty(t, temporaryRoot)
	}}
	firstService := app.NewService(app.Dependencies{
		Fetcher:        fakeFetcher{books: books},
		State:          firstState,
		Sender:         firstSender,
		PhotoProcessor: client,
		MessageLimit:   4096,
		Now:            func() time.Time { return now },
	})

	firstResult, firstErr := firstService.Run(context.Background())

	require.ErrorIs(t, firstErr, deliveryErr)
	require.Equal(t, app.Result{Fetched: 1, New: 1}, firstResult)
	pendingAfterFirst, pendingErr := firstState.Pending(context.Background())
	require.NoError(t, pendingErr)
	require.Len(t, pendingAfterFirst, 1)
	require.Equal(t, server.URL+"/base/published/image", pendingAfterFirst[0].Photos[0].SlinkURL)
	require.NoError(t, firstState.Close())
	secondState, err := store.Open(statePath, now)
	require.NoError(t, err)
	secondSender := &fakeSender{afterSend: func() {
		requireTemporaryRootEmpty(t, temporaryRoot)
	}}
	secondService := app.NewService(app.Dependencies{
		Fetcher:        fakeFetcher{books: books},
		State:          secondState,
		Sender:         secondSender,
		PhotoProcessor: client,
		MessageLimit:   4096,
		Now:            func() time.Time { return now.Add(time.Minute) },
	})

	secondResult, secondErr := secondService.Run(context.Background())

	require.NoError(t, secondErr)
	require.Equal(t, app.Result{Fetched: 1, Sent: 1}, secondResult)
	require.NoError(t, secondState.Close())
	require.EqualValues(t, 1, uploads.Load())
	require.Len(t, secondSender.messages, 1)
	require.Contains(t, secondSender.messages[0], `<tg-slideshow><img src="`+server.URL+
		`/base/published/image"/><figcaption>Обложка</figcaption></tg-slideshow>`)
	require.NotContains(t, secondSender.messages[0], "/base/i/share-code")
	require.Contains(t, secondSender.messages[0], `Смотрите: <a href="http://photo.test/foto.php4?kind=document">Документ</a>`)
	require.NotContains(t, logs.String(), "sk_persistent-api-key")
}

func Test_Service_updatesPersistedSlinkShareURLBeforeRendering(t *testing.T) {
	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/i/old-code":
			assert.Equal(t, http.MethodHead, request.Method)
			http.Redirect(writer, request, "/image/direct.png", http.StatusFound)
		case "/image/direct.png":
			assert.Equal(t, http.MethodHead, request.Method)
			writer.Header().Set("Content-Type", "image/png")
		default:
			t.Errorf("unexpected Slink request %s %s", request.Method, request.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	client, err := slink.NewClientWithOptions(
		server.URL,
		"sk_pending-key",
		"tag-id",
		time.Second,
		slog.New(slog.DiscardHandler),
		slink.Options{},
	)
	require.NoError(t, err)
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"), now)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, state.Close()) })
	book := alib.Book{
		Title:  "Ожидающая книга",
		BuyURL: "https://example.com/pending",
		Photos: []alib.Photo{{
			URL:          "http://photo.test/private-source",
			Caption:      "Обложка",
			SlinkURL:     server.URL + "/i/old-code",
			SlinkProfile: client.Profile(),
		}},
	}
	_, err = state.RecordDiscovered(context.Background(), []alib.Book{book}, now)
	require.NoError(t, err)
	deliveryErr := errors.New("telegram unavailable")
	sender := &fakeSender{err: deliveryErr, failAt: 1}
	service := app.NewService(app.Dependencies{
		Fetcher:        fakeFetcher{},
		State:          state,
		Sender:         sender,
		PhotoProcessor: client,
		MessageLimit:   4096,
		Now:            func() time.Time { return now },
	})

	// When
	_, runErr := service.Run(context.Background())
	pending, pendingErr := state.Pending(context.Background())

	// Then
	require.ErrorIs(t, runErr, deliveryErr)
	require.NoError(t, pendingErr)
	require.Len(t, pending, 1)
	require.Equal(t, server.URL+"/image/direct.png", pending[0].Photos[0].SlinkURL)
	require.Len(t, sender.messages, 1)
	require.Contains(t, sender.messages[0], `<tg-slideshow><img src="`+server.URL+`/image/direct.png"`)
	require.NotContains(t, sender.messages[0], "/i/old-code")
}

func requireTemporaryRootEmpty(t *testing.T, temporaryRoot string) {
	t.Helper()
	entries, err := os.ReadDir(temporaryRoot)
	require.NoError(t, err)
	require.Empty(t, entries)
}

type photoState struct {
	*fakeState
	save  func(context.Context, alib.Book) error
	saved []alib.Book
}

func (state *photoState) SavePrepared(ctx context.Context, book alib.Book) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	state.saved = append(state.saved, book)
	if state.events != nil {
		*state.events = append(*state.events, "save:"+book.BuyURL)
	}
	if state.save != nil {
		return state.save(ctx, book)
	}

	return nil
}

type trackingPhotoProcessor struct {
	client   *slink.Client
	events   *[]string
	prepared []*slink.PreparedBook
}

func newTrackingPhotoProcessor(t *testing.T, server *httptest.Server, events *[]string) *trackingPhotoProcessor {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	client, err := slink.NewClientWithOptions(
		server.URL,
		"sk_api-key",
		"tag-id",
		time.Second,
		logger,
		slink.Options{
			LookupIP: func(context.Context, string) ([]net.IP, error) {
				return []net.IP{net.ParseIP("93.184.216.34")}, nil
			},
			DialContext: serverDialContext(server),
		},
	)
	require.NoError(t, err)

	return &trackingPhotoProcessor{client: client, events: events}
}

func (p *trackingPhotoProcessor) Process(ctx context.Context, book alib.Book) (*slink.PreparedBook, error) {
	if p.events != nil {
		*p.events = append(*p.events, "prepare")
	}
	prepared, err := p.client.Process(ctx, book)
	if prepared != nil {
		p.prepared = append(p.prepared, prepared)
	}

	return prepared, err
}

func (p *trackingPhotoProcessor) Profile() string {
	return p.client.Profile()
}

func photoServer(events *[]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/photo":
			writer.Header().Set("Content-Type", "text/plain")
			if _, err := writer.Write([]byte("\x89PNG\r\n\x1a\n")); err != nil {
				return
			}
		case "/api/external/upload":
			if events != nil {
				*events = append(*events, "upload")
			}
			writer.Header().Set("Content-Type", "application/json")
			if _, err := io.WriteString(writer, `{"url":"/i/share-code"}`); err != nil {
				return
			}
		case "/i/share-code":
			http.Redirect(writer, request, "/published/image", http.StatusFound)
		case "/published/image":
			writer.Header().Set("Content-Type", "image/png")
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
}

func serverDialContext(server *httptest.Server) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
}
