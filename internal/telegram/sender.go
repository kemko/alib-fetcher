// Package telegram sends digest messages through the Telegram Bot API.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"time"
)

// ErrRejected indicates that Telegram returned an unsuccessful API response.
var ErrRejected = errors.New("telegram rejected the message")

// ErrRequest indicates that the Bot API could not be reached.
var ErrRequest = errors.New("telegram request failed")

// RefreshCallbackData identifies digest refresh button presses.
const RefreshCallbackData = "refresh"

const refreshButtonText = "Обновить"

const (
	answerCallbackQueryMethod = "answerCallbackQuery"
	editMessageReplyMarkup    = "editMessageReplyMarkup"
	getUpdatesMethod          = "getUpdates"
	maxAPIResponseBytes       = 1 << 20
	sendMessageMethod         = "sendMessage"
)

// Config contains the Telegram Bot API connection settings.
type Config struct {
	APIBase string
	Token   string
	ChatID  string
	Timeout time.Duration
}

// Sender delivers digest messages through the Telegram Bot API.
type Sender struct {
	client             *http.Client
	endpoint           string
	chatID             string
	longPollTimeoutSec int
}

// NewSender validates the API settings without exposing the bot token.
func NewSender(config Config) (*Sender, error) {
	endpoint, err := url.Parse(config.APIBase)
	if err != nil {
		return nil, fmt.Errorf("parse Telegram API URL: %w", err)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("parse Telegram API URL: unsupported scheme %q", endpoint.Scheme)
	}
	if endpoint.Host == "" || config.Token == "" || config.ChatID == "" {
		return nil, errors.New("create Telegram sender: API host, token, and chat ID are required")
	}
	if config.Timeout <= 0 {
		return nil, errors.New("create Telegram sender: timeout must be positive")
	}

	endpoint.Path = path.Join(endpoint.Path, "bot"+config.Token)
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	return &Sender{
		client:             &http.Client{Timeout: config.Timeout},
		endpoint:           endpoint.String(),
		chatID:             config.ChatID,
		longPollTimeoutSec: longPollTimeout(config.Timeout),
	}, nil
}

// Send posts one HTML-formatted digest message, optionally without a notification sound.
func (s *Sender) Send(ctx context.Context, text string, silent bool, attachRefresh bool) (sendErr error) {
	payload := struct {
		ReplyMarkup         *replyMarkup       `json:"reply_markup,omitempty"`
		ChatID              string             `json:"chat_id"`
		Text                string             `json:"text"`
		ParseMode           string             `json:"parse_mode"`
		LinkPreviewOptions  linkPreviewOptions `json:"link_preview_options"`
		DisableNotification bool               `json:"disable_notification"`
	}{
		ChatID:              s.chatID,
		Text:                text,
		ParseMode:           "HTML",
		DisableNotification: silent,
		LinkPreviewOptions: linkPreviewOptions{
			Disabled: true,
		},
	}
	if attachRefresh {
		payload.ReplyMarkup = &replyMarkup{
			InlineKeyboard: [][]inlineKeyboardButton{
				{
					{
						Text:         refreshButtonText,
						CallbackData: RefreshCallbackData,
					},
				},
			},
		}
	}

	return s.post(ctx, sendMessageMethod, payload, nil)
}

func (s *Sender) post(ctx context.Context, method string, payload any, result any) (postErr error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode Telegram request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.methodEndpoint(method), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create Telegram request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := s.client.Do(request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return fmt.Errorf("call Telegram %s: %w", method, contextErr)
		}
		return ErrRequest
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			postErr = errors.Join(postErr, fmt.Errorf("close Telegram response: %w", closeErr))
		}
	}()

	reader := io.LimitReader(response.Body, maxAPIResponseBytes)
	var apiResult apiResponse
	if decodeErr := json.NewDecoder(reader).Decode(&apiResult); decodeErr != nil {
		return fmt.Errorf("decode Telegram response: %w", decodeErr)
	}
	if response.StatusCode != http.StatusOK || !apiResult.OK {
		if apiResult.Description == "" {
			apiResult.Description = response.Status
		}
		return &rejectedError{
			description: apiResult.Description,
			retryAfter:  time.Duration(apiResult.Parameters.RetryAfter) * time.Second,
		}
	}
	if result == nil || !hasResult(apiResult.Result) {
		return nil
	}
	if decodeErr := json.Unmarshal(apiResult.Result, result); decodeErr != nil {
		return fmt.Errorf("decode Telegram result: %w", decodeErr)
	}

	return nil
}

func (s *Sender) methodEndpoint(method string) string {
	return s.endpoint + "/" + method
}

func hasResult(result json.RawMessage) bool {
	trimmed := bytes.TrimSpace(result)

	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func longPollTimeout(timeout time.Duration) int {
	seconds := int(timeout / time.Second)
	if seconds <= 1 {
		return 1
	}

	return seconds - 1
}

type linkPreviewOptions struct {
	Disabled bool `json:"is_disabled"`
}

type replyMarkup struct {
	InlineKeyboard [][]inlineKeyboardButton `json:"inline_keyboard"`
}

type inlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type apiResponse struct {
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
	OK bool `json:"ok"`
}

type rejectedError struct {
	description string
	retryAfter  time.Duration
}

func (e *rejectedError) Error() string {
	return fmt.Sprintf("%s: %s", ErrRejected, e.description)
}

func (e *rejectedError) Unwrap() error {
	return ErrRejected
}

func (e *rejectedError) RetryAfter() time.Duration {
	return e.retryAfter
}
