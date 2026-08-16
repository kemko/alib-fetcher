package telegram

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	sender := newTestSenderWithBody(body)

	// When
	err := sender.Send(ctx, "digest", false, false)

	// Then
	require.ErrorIs(t, err, ErrRequest)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, closeErr)
	assert.NotContains(t, err.Error(), "test-token")
	assert.NotContains(t, err.Error(), "api.telegram.org")
}

func Test_Sender_ignores_close_failure_after_successful_response(t *testing.T) {
	t.Parallel()

	// Given
	body := &testResponseBody{
		reader:   bytes.NewReader([]byte(`{"ok":true}`)),
		closeErr: errors.New("close response failed"),
	}
	sender := newTestSenderWithBody(body)

	// When
	err := sender.Send(context.Background(), "digest", false, false)

	// Then
	require.NoError(t, err)
}

func Test_Sender_hides_transport_error_details(t *testing.T) {
	t.Parallel()

	// Given
	transportErr := errors.New("Post https://api.telegram.org/bottest-token/sendMessage failed")
	sender := newTestSenderWithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportErr
	}))

	// When
	err := sender.Send(context.Background(), "digest", false, false)

	// Then
	require.ErrorIs(t, err, ErrRequest)
	require.ErrorIs(t, err, transportErr)
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

func newTestSenderWithBody(body io.ReadCloser) *Sender {
	return newTestSenderWithTransport(roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			Status:     "200 OK",
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       body,
			Request:    request,
		}, nil
	}))
}

func newTestSenderWithTransport(transport http.RoundTripper) *Sender {
	return &Sender{
		client:   &http.Client{Transport: transport},
		endpoint: "https://api.telegram.org/bottest-token",
		chatID:   "-100123",
	}
}
