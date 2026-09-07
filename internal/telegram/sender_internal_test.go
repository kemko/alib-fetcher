package telegram

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	telegrambot "github.com/go-telegram/bot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_Client_timeout_change_preserves_acknowledged_queued_callbacks(t *testing.T) {
	t.Parallel()

	// Given: pause the SDK worker so polling acknowledges an unhandled callback.
	var polls atomic.Int32
	acknowledged := make(chan struct{})
	restarted := make(chan struct{})
	sender := newTestSenderWithTransport(t, roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		assert.NoError(t, request.ParseMultipartForm(maxAPIResponseBytes))
		switch polls.Add(1) {
		case 1:
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(bytes.NewBufferString(
					`{"ok":true,"result":[{"update_id":100,"callback_query":{"id":"queued","data":"refresh"}}]}`,
				)),
				Request: request,
			}, nil
		case 2:
			assert.Equal(t, "101", request.FormValue("offset"))
			close(acknowledged)
		case 3:
			assert.Equal(t, "101", request.FormValue("offset"))
			assert.Equal(t, "6", request.FormValue("timeout"))
			close(restarted)
		default:
			t.Error("unexpected polling request")
		}
		<-request.Context().Done()
		return nil, request.Context().Err()
	}))
	client := sender.client
	telegrambot.WithWorkers(0)(client.bot)
	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		client.ListenCallbacks(firstCtx, nil, nil)
	}()
	require.Eventually(t, func() bool { return polls.Load() == 2 }, time.Second, time.Millisecond)
	<-acknowledged
	firstCancel()
	<-firstDone

	// When
	require.NoError(t, client.SetTimeout(7*time.Second))
	telegrambot.WithWorkers(1)(client.bot)
	secondCtx, secondCancel := context.WithCancel(context.Background())
	defer secondCancel()
	secondDone := make(chan struct{})
	callbacks := make(chan Callback, 1)
	go func() {
		defer close(secondDone)
		client.ListenCallbacks(secondCtx, func(_ context.Context, callback Callback) {
			callbacks <- callback
		}, nil)
	}()

	// Then
	require.Eventually(t, func() bool { return len(callbacks) == 1 }, time.Second, time.Millisecond)
	require.Equal(t, "queued", (<-callbacks).ID)
	<-restarted
	secondCancel()
	<-secondDone
}

func Test_Sender_classifies_response_body_read_failure(t *testing.T) {
	t.Parallel()

	// Given
	readErr := errors.New("read https://api.telegram.org/bottest-token failed")
	closeErr := errors.New("close response failed")
	ctx, cancel := context.WithCancel(context.Background())
	body := &testResponseBody{
		readErr:  readErr,
		closeErr: closeErr,
		beforeRead: func() {
			cancel()
		},
	}
	sender := newTestSenderWithBody(t, body)

	// When
	err := sender.Send(ctx, "digest", false, false)

	// Then
	require.ErrorIs(t, err, ErrRequest)
	require.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, err.Error(), "read ***/bot*** failed")
	assert.NotContains(t, err.Error(), "test-token")
	assert.NotContains(t, err.Error(), "api.telegram.org")
}

func Test_Sender_does_not_report_close_failure_as_poll_error(t *testing.T) {
	t.Parallel()

	// Given
	body := &testResponseBody{
		reader:   bytes.NewReader([]byte(`{"ok":true,"result":{}}`)),
		closeErr: errors.New("close response failed"),
	}
	sender := newTestSenderWithBody(t, body)

	// When
	err := sender.Send(context.Background(), "digest", false, false)

	// Then
	require.NoError(t, err)
	assert.Empty(t, sender.client.sdkErrors)
}

func Test_Sender_limits_API_response_read(t *testing.T) {
	t.Parallel()

	// Given
	reader := &countingReader{}
	sender := newTestSenderWithBody(t, &testResponseBody{reader: reader})

	// When
	err := sender.Send(context.Background(), "digest", false, false)

	// Then
	require.Error(t, err)
	assert.Contains(t, err.Error(), "response exceeds")
	require.Equal(t, maxAPIResponseBytes+1, reader.bytesRead)
}

func Test_Sender_accepts_API_response_at_size_limit(t *testing.T) {
	t.Parallel()

	// Given
	prefix := []byte(`{"ok":true,"result":{}}`)
	response := append(prefix, bytes.Repeat([]byte(" "), maxAPIResponseBytes-len(prefix))...)
	sender := newTestSenderWithBody(t, &testResponseBody{reader: bytes.NewReader(response)})

	// When
	err := sender.Send(context.Background(), "digest", false, false)

	// Then
	require.NoError(t, err)
}

func Test_Sender_includes_sanitized_transport_error_details(t *testing.T) {
	t.Parallel()

	// Given
	transportErr := errors.New("Post https://api.telegram.org/bottest-token/sendRichMessage failed")
	sender := newTestSenderWithTransport(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportErr
	}))

	// When
	err := sender.Send(context.Background(), "digest", false, false)

	// Then
	require.ErrorIs(t, err, ErrRequest)
	require.ErrorIs(t, err, transportErr)
	assert.Contains(t, err.Error(), "Post ***/bot***/sendRichMessage failed")
	assert.NotContains(t, err.Error(), "test-token")
	assert.NotContains(t, err.Error(), "api.telegram.org")
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type testResponseBody struct {
	reader     io.Reader
	readErr    error
	closeErr   error
	beforeRead func()
}

type countingReader struct {
	bytesRead int
}

func (reader *countingReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = ' '
	}
	reader.bytesRead += len(buffer)

	return len(buffer), nil
}

func (body *testResponseBody) Read(buffer []byte) (int, error) {
	if body.beforeRead != nil {
		body.beforeRead()
		body.beforeRead = nil
	}
	if body.readErr != nil {
		return 0, body.readErr
	}

	return body.reader.Read(buffer)
}

func (body *testResponseBody) Close() error {
	return body.closeErr
}

func newTestSenderWithBody(t *testing.T, body io.ReadCloser) *Sender {
	t.Helper()

	return newTestSenderWithTransport(t, roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			Status:     "200 OK",
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       body,
			Request:    request,
		}, nil
	}))
}

func newTestSenderWithTransport(t *testing.T, transport http.RoundTripper) *Sender {
	t.Helper()
	sender, err := newSender(ClientConfig{
		Token:   "test-token",
		Timeout: 2 * time.Second,
	}, &http.Client{Transport: transport}, "-100123")
	require.NoError(t, err)

	return sender
}
