package app_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/app"
	"github.com/kemko/alib-fetcher/internal/slink"

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
		pending:     []alib.Book{book},
		recordedNew: 1,
		events:      &events,
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
	require.Equal(t, app.Result{Fetched: 1, New: 1, Sent: 1}, result)
	require.Len(t, photoState.saved, 1)
	require.Contains(t, sender.messages[0], "<tg-slideshow>")
	require.Equal(t, []string{"fetch", "record", "pending", "prepare", "upload", "save:" + book.BuyURL, "send", "mark:" + book.BuyURL}, events)
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
		fakeState: &fakeState{pending: []alib.Book{book}, recordedNew: 1},
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
	require.Equal(t, app.Result{Fetched: 1, New: 1}, result)
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
		fakeState: &fakeState{pending: []alib.Book{book}, recordedNew: 1},
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
	require.Equal(t, app.Result{Fetched: 1, New: 1}, result)
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
		"api-key",
		"tag-id",
		time.Second,
		logger,
		slink.Options{
			HTTPClient: &http.Client{Transport: rewriteTransport{target: server.URL}},
			LookupIP: func(context.Context, string) ([]net.IP, error) {
				return []net.IP{net.ParseIP("93.184.216.34")}, nil
			},
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
			if _, err := io.WriteString(writer, `{"url":"/published/image"}`); err != nil {
				return
			}
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
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
