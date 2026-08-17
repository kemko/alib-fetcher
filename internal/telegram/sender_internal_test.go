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

func Test_Sender_limits_API_response_read(t *testing.T) {
	t.Parallel()

	// Given
	reader := &countingReader{}
	sender := newTestSenderWithBody(&testResponseBody{reader: reader})

	// When
	err := sender.Send(context.Background(), "digest", false, false)

	// Then
	require.Error(t, err)
	assert.Contains(t, err.Error(), "response exceeds")
	require.Equal(t, maxAPIResponseBytes+1, reader.bytesRead)
}

func Test_parseAPIResponse_accepts_response_at_size_limit(t *testing.T) {
	t.Parallel()

	// Given
	prefix := []byte(`{"ok":true}`)
	body := append(prefix, bytes.Repeat([]byte(" "), maxAPIResponseBytes-len(prefix))...)
	response := &http.Response{Status: "200 OK", StatusCode: http.StatusOK}

	// When
	result, err := parseAPIResponse(response, body)

	// Then
	require.NoError(t, err)
	require.True(t, result.OK)
}

func Test_parseAPIResponse_classifies_oversized_HTTP_error_as_rejected(t *testing.T) {
	t.Parallel()

	// Given
	body := bytes.Repeat([]byte("x"), maxAPIResponseBytes+1)
	response := &http.Response{Status: "502 Bad Gateway", StatusCode: http.StatusBadGateway}

	// When
	_, err := parseAPIResponse(response, body)

	// Then
	require.ErrorIs(t, err, ErrRejected)
	require.Contains(t, err.Error(), "502 Bad Gateway")
}

func Test_Sender_hides_transport_error_details(t *testing.T) {
	t.Parallel()

	// Given
	transportErr := errors.New("Post https://api.telegram.org/bottest-token/sendRichMessage failed")
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
