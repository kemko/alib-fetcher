package telegram_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kemko/alib-fetcher/internal/telegram"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_Sender_posts_silent_rich_HTML_message(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/bottest-token/sendRichMessage", request.URL.Path)
		payload := readMultipartPayload(t, request)
		assert.Equal(t, "-100123", payload["chat_id"])
		assert.JSONEq(t, `{"html":"<b>digest</b>"}`, payload["rich_message"])
		assert.Equal(t, "true", payload["disable_notification"])
		assert.NotContains(t, payload, "reply_markup")
		assert.NotContains(t, payload, "text")
		assert.NotContains(t, payload, "parse_mode")
		assert.NotContains(t, payload, "link_preview_options")
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"ok":true,"result":{}}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	sender, err := newTestSender(server.URL)
	require.NoError(t, err)

	// When
	err = sender.Send(context.Background(), "<b>digest</b>", true, false)

	// Then
	require.NoError(t, err)
}

func Test_Sender_posts_audible_HTML_message_with_notification_enabled(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/bottest-token/sendRichMessage", request.URL.Path)
		payload := readMultipartPayload(t, request)
		assert.NotContains(t, payload, "disable_notification")
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"ok":true,"result":{}}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	sender, err := newTestSender(server.URL)
	require.NoError(t, err)

	// When
	err = sender.Send(context.Background(), "<b>digest</b>", false, false)

	// Then
	require.NoError(t, err)
}

func Test_Sender_posts_refresh_button_when_requested(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/bottest-token/sendRichMessage", request.URL.Path)
		payload := readMultipartPayload(t, request)
		var replyMarkup struct {
			InlineKeyboard [][]struct {
				Text         string `json:"text"`
				CallbackData string `json:"callback_data"`
			} `json:"inline_keyboard"`
		}
		assert.NoError(t, json.Unmarshal([]byte(payload["reply_markup"]), &replyMarkup))
		if !assert.Len(t, replyMarkup.InlineKeyboard, 1) {
			return
		}
		if !assert.Len(t, replyMarkup.InlineKeyboard[0], 1) {
			return
		}
		assert.Equal(t, "Обновить", replyMarkup.InlineKeyboard[0][0].Text)
		assert.Equal(t, telegram.RefreshCallbackData, replyMarkup.InlineKeyboard[0][0].CallbackData)
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write([]byte(`{"ok":true,"result":{}}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	sender, err := newTestSender(server.URL)
	require.NoError(t, err)

	// When
	err = sender.Send(context.Background(), "<b>digest</b>", false, true)

	// Then
	require.NoError(t, err)
}

func Test_Sender_reports_response_errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		expectedError error
		name          string
		body          string
		description   string
		status        int
	}{
		{
			name:          "API rejection description",
			status:        http.StatusOK,
			body:          `{"ok":false,"error_code":400,"description":"chat not found"}`,
			expectedError: telegram.ErrRejected,
			description:   "chat not found",
		},
		{
			name:          "chat migration",
			status:        http.StatusBadRequest,
			body:          `{"ok":false,"error_code":400,"description":"group chat was upgraded","parameters":{"migrate_to_chat_id":-100456}}`,
			expectedError: telegram.ErrRejected,
			description:   "migrate_to_chat_id -100456",
		},
		{
			name:          "HTTP rejection without description",
			status:        http.StatusBadGateway,
			body:          `{"ok":false,"error_code":502}`,
			expectedError: telegram.ErrRejected,
			description:   "502",
		},
		{
			name:          "success body with unsuccessful HTTP status",
			status:        http.StatusBadGateway,
			body:          `{"ok":true,"result":{}}`,
			expectedError: telegram.ErrRejected,
			description:   "Bad Gateway",
		},
		{
			name:        "trailing data",
			status:      http.StatusOK,
			body:        `{"ok":true}{"ok":true}`,
			description: "decode Telegram response",
		},
		{
			name:        "invalid JSON",
			status:      http.StatusOK,
			body:        `not json`,
			description: "decode Telegram response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Given
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(tt.status)
				_, err := writer.Write([]byte(tt.body))
				assert.NoError(t, err)
			}))
			t.Cleanup(server.Close)
			sender, err := newTestSender(server.URL)
			require.NoError(t, err)

			// When
			err = sender.Send(context.Background(), "digest", false, false)

			// Then
			require.Error(t, err)
			if tt.expectedError != nil {
				require.ErrorIs(t, err, tt.expectedError)
			}
			require.Contains(t, err.Error(), tt.description)
		})
	}
}

func Test_Sender_redacts_token_from_rejection(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, err := writer.Write(fmt.Appendf(
			nil,
			`{"ok":false,"error_code":400,"description":"denied test-token at http://%s"}`,
			request.Host,
		))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	sender, err := newTestSenderWithChat(server.URL, "missing")
	require.NoError(t, err)

	// When
	err = sender.Send(context.Background(), "digest", false, false)

	// Then
	require.ErrorIs(t, err, telegram.ErrRejected)
	assert.NotContains(t, err.Error(), "test-token")
}

func Test_Sender_exposes_Telegram_retry_delay(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, err := writer.Write([]byte(`{
			"ok":false,
			"error_code":429,
			"description":"Too Many Requests: retry after 1",
			"parameters":{"retry_after":1}
		}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	sender, err := newTestSender(server.URL)
	require.NoError(t, err)

	// When
	err = sender.Send(context.Background(), "digest", false, false)

	// Then
	require.ErrorIs(t, err, telegram.ErrRejected)
	var retryable interface {
		RetryAfter() time.Duration
	}
	require.ErrorAs(t, err, &retryable)
	require.Equal(t, time.Second, retryable.RetryAfter())
}

func Test_NewSender_validates_configuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		token   string
		timeout time.Duration
	}{
		{
			name:    "missing token",
			timeout: 2 * time.Second,
		},
		{
			name:  "non-positive timeout",
			token: "test-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// When
			client, err := telegram.NewClient(telegram.ClientConfig{
				Token:   tt.token,
				Timeout: tt.timeout,
			})

			// Then
			require.Error(t, err)
			require.Nil(t, client)
		})
	}
}

func Test_NewSender_accepts_short_positive_timeout(t *testing.T) {
	t.Parallel()

	// When
	client, err := telegram.NewClient(telegram.ClientConfig{
		Token:   "test-token",
		Timeout: time.Millisecond,
	})

	// Then
	require.NoError(t, err)
	require.NotNil(t, client)
}

func Test_Client_rejects_an_empty_chat_id(t *testing.T) {
	t.Parallel()

	client, err := telegram.NewClient(telegram.ClientConfig{
		Token:   "test-token",
		Timeout: time.Second,
	})
	require.NoError(t, err)

	sender, err := client.NewSender(" ")
	require.Error(t, err)
	require.Nil(t, sender)
}

func Test_Client_shares_one_SDK_client_between_chat_senders(t *testing.T) {
	t.Parallel()

	// Given
	var requests []string
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		require.NoError(t, request.ParseMultipartForm(1<<20))
		requests = append(requests, fmt.Sprintf("%s %s", request.URL, request.FormValue("chat_id")))
		return &http.Response{
			Status:     "200 OK",
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`)),
			Request:    request,
		}, nil
	})
	client, err := telegram.NewClient(telegram.ClientConfig{
		Token:      "shared-token",
		Timeout:    2 * time.Second,
		HTTPClient: &http.Client{Transport: transport},
	})
	require.NoError(t, err)
	first, err := client.NewSender("-100123")
	require.NoError(t, err)
	second, err := client.NewSender("@books")
	require.NoError(t, err)

	// When
	require.NoError(t, first.Send(context.Background(), "first", false, false))
	require.NoError(t, second.Send(context.Background(), "second", false, false))

	// Then
	require.Equal(t, []string{
		"https://api.telegram.org/botshared-token/sendRichMessage -100123",
		"https://api.telegram.org/botshared-token/sendRichMessage @books",
	}, requests)
}

func Test_Client_uses_each_token_with_the_standard_API_endpoint(t *testing.T) {
	t.Parallel()

	// Given
	var requests []string
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		require.NoError(t, request.ParseMultipartForm(1<<20))
		requests = append(requests, fmt.Sprintf("%s %s", request.URL, request.FormValue("chat_id")))
		return &http.Response{
			Status:     "200 OK",
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`)),
			Request:    request,
		}, nil
	})
	newClient := func(token string) *telegram.Client {
		client, clientErr := telegram.NewClient(telegram.ClientConfig{
			Token:      token,
			Timeout:    2 * time.Second,
			HTTPClient: &http.Client{Transport: transport},
		})
		require.NoError(t, clientErr)

		return client
	}
	first := newClient("first-token")
	firstSender, err := first.NewSender("-100123")
	require.NoError(t, err)
	second := newClient("second-token")
	secondSender, err := second.NewSender("-100124")
	require.NoError(t, err)

	// When
	require.NoError(t, firstSender.Send(context.Background(), "first", false, false))
	require.NoError(t, secondSender.Send(context.Background(), "second", false, false))

	// Then
	require.Equal(t, []string{
		"https://api.telegram.org/botfirst-token/sendRichMessage -100123",
		"https://api.telegram.org/botsecond-token/sendRichMessage -100124",
	}, requests)
}

func Test_Sender_uses_HTTP_status_for_undecodable_HTTP_rejection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "plain text", body: "upstream unavailable"},
		{name: "malformed JSON", body: `{"ok":false`},
		{name: "oversized", body: `{"ok":false}` + strings.Repeat(" ", 1<<20)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Given
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusBadGateway)
				_, err := writer.Write([]byte(tt.body))
				assert.NoError(t, err)
			}))
			t.Cleanup(server.Close)
			sender, err := newTestSender(server.URL)
			require.NoError(t, err)

			// When
			err = sender.Send(context.Background(), "digest", false, false)

			// Then
			require.Error(t, err)
			require.ErrorIs(t, err, telegram.ErrRejected)
			assert.Contains(t, err.Error(), "502 Bad Gateway")
		})
	}
}

func Test_Sender_rejects_oversized_API_response(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, err := writer.Write([]byte(`{"ok":true}` + strings.Repeat(" ", 1<<20)))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	sender, err := newTestSender(server.URL)
	require.NoError(t, err)

	// When
	err = sender.Send(context.Background(), "digest", false, false)

	// Then
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds 1048576 bytes")
	assert.NotErrorIs(t, err, telegram.ErrRequest)
	assert.NotErrorIs(t, err, telegram.ErrRejected)
}

func Test_Sender_returns_request_error_for_transport_failure(t *testing.T) {
	t.Parallel()

	// Given
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	sender, err := newTestSender(server.URL)
	require.NoError(t, err)
	server.Close()

	// When
	err = sender.Send(context.Background(), "digest", false, false)

	// Then
	require.ErrorIs(t, err, telegram.ErrRequest)
}

func Test_Sender_returns_context_error_when_request_is_canceled(t *testing.T) {
	t.Parallel()

	// Given
	client, err := telegram.NewClient(telegram.ClientConfig{
		Token:   "test-token",
		Timeout: 2 * time.Second,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// When
	sender, err := client.NewSender("-100123")
	require.NoError(t, err)
	err = sender.Send(ctx, "digest", false, false)

	// Then
	require.ErrorIs(t, err, context.Canceled)
}

func readMultipartPayload(t *testing.T, request *http.Request) map[string]string {
	t.Helper()
	require.NoError(t, request.ParseMultipartForm(1<<20))
	payload := make(map[string]string, len(request.MultipartForm.Value))
	for key, values := range request.MultipartForm.Value {
		require.Len(t, values, 1)
		payload[key] = values[0]
	}

	return payload
}
