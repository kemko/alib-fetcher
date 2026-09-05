package telegram_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kemko/alib-fetcher/internal/telegram"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_Sender_listens_for_registered_refresh_callbacks(t *testing.T) {
	t.Parallel()

	// Given
	var requestCount atomic.Int32
	secondPoll := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if requestCount.Add(1) > 1 {
			payload := readMultipartPayload(t, request)
			assert.Equal(t, "104", payload["offset"])
			select {
			case secondPoll <- struct{}{}:
			default:
			}
			writer.Header().Set("Content-Type", "application/json")
			_, err := writer.Write([]byte(`{"ok":true,"result":[]}`))
			assert.NoError(t, err)

			return
		}
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "/bottest-token/getUpdates", request.URL.Path)
		payload := readMultipartPayload(t, request)
		assert.Equal(t, "1", payload["offset"])
		assert.Equal(t, "4", payload["timeout"])
		assert.JSONEq(t, `["callback_query"]`, payload["allowed_updates"])
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{
			"ok": true,
			"result": [
				{
					"update_id": 100,
					"callback_query": {"id": "unknown", "data": "unknown"}
				},
				{
					"update_id": 101,
					"callback_query": {
						"id": "callback-1",
						"data": "refresh",
						"message": {
							"message_id": 77,
							"date": 1,
							"chat": {"id": -100123, "username": "books"}
						}
					}
				},
				{
					"update_id": 102,
					"callback_query": {
						"id": "callback-2",
						"data": "refresh",
						"message": {
							"message_id": 78,
							"date": 0,
							"chat": {"id": -100124, "username": "archive"}
						}
					}
				},
				{
					"update_id": 103,
					"callback_query": {"id": "callback-3", "data": "refresh"}
				}
			]
		}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	sender, err := telegram.NewSender(telegram.Config{
		APIBase: server.URL,
		Token:   "test-token",
		ChatID:  "-100123",
		Timeout: 5 * time.Second,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	callbacks := make(chan telegram.Callback, 3)
	errors := make(chan error, 1)
	done := make(chan struct{})
	callbacksHandled := make(chan struct{})
	var callbackCount atomic.Int32

	// When
	go func() {
		defer close(done)
		sender.ListenCallbacks(ctx, func(_ context.Context, callback telegram.Callback) {
			callbacks <- callback
			if callbackCount.Add(1) == 3 {
				close(callbacksHandled)
			}
		}, func(_ context.Context, err error) {
			errors <- err
		})
	}()
	waitForSignal(t, callbacksHandled, "refresh callbacks were not handled")
	waitForSignal(t, secondPoll, "second callback poll did not start")
	cancel()
	waitForListener(t, done)

	// Then
	close(callbacks)
	assert.Equal(t, []telegram.Callback{
		{
			ID:                  "callback-1",
			Data:                telegram.RefreshCallbackData,
			MessageChatUsername: "books",
			MessageChatID:       -100123,
			MessageID:           77,
		},
		{
			ID:                  "callback-2",
			Data:                telegram.RefreshCallbackData,
			MessageChatUsername: "archive",
			MessageChatID:       -100124,
			MessageID:           78,
		},
		{
			ID:   "callback-3",
			Data: telegram.RefreshCallbackData,
		},
	}, collectCallbacks(callbacks))
	assert.Empty(t, errors)
	assert.Equal(t, int32(3), callbackCount.Load())
}

func Test_Sender_completes_API_calls_while_polling_is_held(t *testing.T) {
	t.Parallel()

	// Given
	pollStarted := make(chan struct{}, 1)
	pollRelease := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, err := io.Copy(io.Discard, request.Body)
		assert.NoError(t, err)
		if request.URL.Path == "/bottest-token/getUpdates" {
			select {
			case pollStarted <- struct{}{}:
			default:
			}
			select {
			case <-pollRelease:
			case <-request.Context().Done():
				return
			}
			writeTelegramResponse(t, writer, `{"ok":true,"result":[]}`)

			return
		}
		if request.URL.Path == "/bottest-token/sendRichMessage" {
			writeTelegramResponse(t, writer, `{"ok":true,"result":{}}`)

			return
		}
		assert.Equal(t, "/bottest-token/answerCallbackQuery", request.URL.Path)
		writeTelegramResponse(t, writer, `{"ok":true,"result":true}`)
	}))
	t.Cleanup(server.Close)
	sender, err := telegram.NewSender(telegram.Config{
		APIBase: server.URL,
		Token:   "test-token",
		ChatID:  "-100123",
		Timeout: 5 * time.Second,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	listenerDone := make(chan struct{})

	// When
	go func() {
		defer close(listenerDone)
		sender.ListenCallbacks(ctx, nil, nil)
	}()
	waitForSignal(t, pollStarted, "polling request did not start")
	sendDone := make(chan error, 1)
	answerDone := make(chan error, 1)
	go func() {
		sendDone <- sender.Send(context.Background(), "digest", false, false)
	}()
	go func() {
		answerDone <- sender.AnswerCallback(context.Background(), "callback-1", "Started")
	}()

	// Then
	select {
	case sendErr := <-sendDone:
		require.NoError(t, sendErr)
	case <-time.After(time.Second):
		t.Fatal("send remained blocked by polling")
	}
	select {
	case answerErr := <-answerDone:
		require.NoError(t, answerErr)
	case <-time.After(time.Second):
		t.Fatal("callback answer remained blocked by polling")
	}
	cancel()
	close(pollRelease)
	waitForListener(t, listenerDone)
}

func Test_Sender_listener_applies_short_HTTP_timeout_to_polling(t *testing.T) {
	t.Parallel()

	// Given
	var requestCount atomic.Int32
	requestStarted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		requestCount.Add(1)
		select {
		case requestStarted <- struct{}{}:
		default:
		}
		assert.Equal(t, "/bottest-token/getUpdates", request.URL.Path)
		payload := readMultipartPayload(t, request)
		assert.Equal(t, "1", payload["timeout"])
		assert.JSONEq(t, `["callback_query"]`, payload["allowed_updates"])
		time.Sleep(750 * time.Millisecond)
	}))
	t.Cleanup(server.Close)
	sender, err := telegram.NewSender(telegram.Config{
		APIBase: server.URL,
		Token:   "test-token",
		ChatID:  "-100123",
		Timeout: 500 * time.Millisecond,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	callbacks := make(chan telegram.Callback, 1)
	errors := make(chan error, 1)
	done := make(chan struct{})

	// When
	go func() {
		defer close(done)
		sender.ListenCallbacks(ctx, func(_ context.Context, callback telegram.Callback) {
			callbacks <- callback
		}, func(_ context.Context, err error) {
			errors <- err
			cancel()
		})
	}()
	waitForListener(t, done)

	// Then
	pollErr := <-errors
	waitForSignal(t, requestStarted, "polling request did not reach the server")
	require.ErrorIs(t, pollErr, telegram.ErrRequest)
	assert.ErrorIs(t, pollErr, context.DeadlineExceeded)
	assert.Empty(t, callbacks)
	assert.Equal(t, int32(1), requestCount.Load())
}

func Test_Sender_answers_callback_query(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "/bottest-token/answerCallbackQuery", request.URL.Path)
		payload := readMultipartPayload(t, request)
		assert.Equal(t, "callback-1", payload["callback_query_id"])
		assert.Equal(t, "Started", payload["text"])
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"ok":true,"result":true}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	sender, err := newTestSender(server.URL)
	require.NoError(t, err)

	// When
	err = sender.AnswerCallback(context.Background(), "callback-1", "Started")

	// Then
	require.NoError(t, err)
}

func Test_Sender_removes_reply_markup(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "/bottest-token/editMessageReplyMarkup", request.URL.Path)
		payload := readMultipartPayload(t, request)
		assert.Equal(t, "-100123", payload["chat_id"])
		assert.Equal(t, "77", payload["message_id"])
		var replyMarkup struct {
			InlineKeyboard [][]struct{} `json:"inline_keyboard"`
		}
		if !assert.NoError(t, json.Unmarshal([]byte(payload["reply_markup"]), &replyMarkup)) {
			return
		}
		if !assert.NotNil(t, replyMarkup.InlineKeyboard) {
			return
		}
		assert.Empty(t, replyMarkup.InlineKeyboard)
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"ok":true,"result":true}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	sender, err := newTestSender(server.URL)
	require.NoError(t, err)

	// When
	err = sender.RemoveReplyMarkup(context.Background(), -100123, 77)

	// Then
	require.NoError(t, err)
}

func Test_Sender_returns_rejection_for_callback_API_failure(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, err := writer.Write([]byte(`{"ok":false,"error_code":400,"description":"query is too old"}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	sender, err := newTestSender(server.URL)
	require.NoError(t, err)

	// When
	err = sender.AnswerCallback(context.Background(), "callback-1", "Started")

	// Then
	require.ErrorIs(t, err, telegram.ErrRejected)
	require.Contains(t, err.Error(), "query is too old")
}

func Test_Sender_listener_reports_poll_error_and_recovers(t *testing.T) {
	t.Parallel()

	// Given
	var requestCount atomic.Int32
	errorReported := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch requestCount.Add(1) {
		case 1:
			_, err := writer.Write([]byte(`{
				"ok":false,
				"error_code":400,
				"description":"test-token rejected"
			}`))
			assert.NoError(t, err)

			return
		case 2:
			<-errorReported
		default:
			_, err := writer.Write([]byte(`{"ok":true,"result":[]}`))
			assert.NoError(t, err)

			return
		}
		_, err := writer.Write([]byte(`{
			"ok":true,
			"result":[{
				"update_id":42,
				"callback_query":{"id":"callback-1","data":"refresh"}
			}]
		}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	sender, err := telegram.NewSender(telegram.Config{
		APIBase: server.URL,
		Token:   "test-token",
		ChatID:  "-100123",
		Timeout: 2 * time.Second,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errors := make(chan error, 1)
	callbacks := make(chan telegram.Callback, 1)
	done := make(chan struct{})

	// When
	go func() {
		defer close(done)
		sender.ListenCallbacks(ctx, func(_ context.Context, callback telegram.Callback) {
			callbacks <- callback
			cancel()
		}, func(_ context.Context, err error) {
			errors <- err
			close(errorReported)
		})
	}()
	waitForListener(t, done)

	// Then
	pollErr := <-errors
	require.ErrorIs(t, pollErr, telegram.ErrRejected)
	assert.NotContains(t, pollErr.Error(), "test-token")
	assert.NotContains(t, pollErr.Error(), server.URL)
	assert.Equal(t, telegram.Callback{ID: "callback-1", Data: telegram.RefreshCallbackData}, <-callbacks)
	assert.GreaterOrEqual(t, requestCount.Load(), int32(2))
}

func Test_Sender_listener_limits_poll_response_read(t *testing.T) {
	t.Parallel()

	// Given
	requestStarted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/bottest-token/getUpdates", request.URL.Path)
		select {
		case requestStarted <- struct{}{}:
		default:
		}
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write(bytes.Repeat([]byte(" "), (1<<20)+1))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	sender, err := telegram.NewSender(telegram.Config{
		APIBase: server.URL,
		Token:   "test-token",
		ChatID:  "-100123",
		Timeout: 2 * time.Second,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errors := make(chan error, 1)
	done := make(chan struct{})

	// When
	go func() {
		defer close(done)
		sender.ListenCallbacks(ctx, nil, func(_ context.Context, err error) {
			errors <- err
			cancel()
		})
	}()
	waitForSignal(t, requestStarted, "polling request did not reach the server")
	pollErr := <-errors
	waitForListener(t, done)

	// Then
	require.Error(t, pollErr)
	assert.Contains(t, pollErr.Error(), "response exceeds")
}

func Test_Sender_listener_stops_when_context_is_canceled(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("request should not be sent")
	}))
	t.Cleanup(server.Close)
	sender, err := newTestSender(server.URL)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})

	// When
	go func() {
		defer close(done)
		sender.ListenCallbacks(ctx, func(context.Context, telegram.Callback) {}, func(context.Context, error) {})
	}()

	// Then
	waitForListener(t, done)
}

func collectCallbacks(callbacks <-chan telegram.Callback) []telegram.Callback {
	items := make([]telegram.Callback, 0, len(callbacks))
	for callback := range callbacks {
		items = append(items, callback)
	}

	return items
}

func waitForListener(t *testing.T, done <-chan struct{}) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("callback listener did not stop")
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}, failureMessage string) {
	t.Helper()

	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal(failureMessage)
	}
}

func newTestSender(apiBase string) (*telegram.Sender, error) {
	return telegram.NewSender(telegram.Config{
		APIBase: apiBase,
		Token:   "test-token",
		ChatID:  "-100123",
		Timeout: 2 * time.Second,
	})
}

func writeTelegramResponse(t *testing.T, writer http.ResponseWriter, body string) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	_, err := writer.Write([]byte(body))
	assert.NoError(t, err)
}
