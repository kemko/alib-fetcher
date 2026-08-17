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
	sendRichMessageMethod     = "sendRichMessage"
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

// Send posts one rich HTML digest message, optionally without a notification sound.
func (s *Sender) Send(ctx context.Context, text string, silent bool, attachRefresh bool) (sendErr error) {
	payload := struct {
		ReplyMarkup         *replyMarkup     `json:"reply_markup,omitempty"`
		ChatID              string           `json:"chat_id"`
		RichMessage         inputRichMessage `json:"rich_message"`
		DisableNotification bool             `json:"disable_notification"`
	}{
		ChatID:              s.chatID,
		RichMessage:         inputRichMessage{HTML: text},
		DisableNotification: silent,
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

	return s.post(ctx, sendRichMessageMethod, payload, nil)
}

func (s *Sender) post(ctx context.Context, method string, payload any, result any) (postErr error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode Telegram request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create Telegram request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := s.client.Do(request)
	if err != nil {
		return requestError(ctx, "call Telegram "+method, err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil && postErr != nil {
			postErr = errors.Join(postErr, &safeCauseError{message: "close Telegram response", cause: closeErr})
		}
	}()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponseBytes+1))
	if err != nil {
		return requestError(ctx, "read Telegram response", err)
	}
	apiResult, err := parseAPIResponse(response, responseBody)
	if err != nil {
		return err
	}
	if result == nil || !hasResult(apiResult.Result) {
		return nil
	}
	if resultDecodeErr := json.Unmarshal(apiResult.Result, result); resultDecodeErr != nil {
		return fmt.Errorf("decode Telegram result: %w", resultDecodeErr)
	}

	return nil
}

func parseAPIResponse(response *http.Response, body []byte) (apiResponse, error) {
	if len(body) > maxAPIResponseBytes {
		if response.StatusCode != http.StatusOK {
			return apiResponse{}, newRejectedError(response, apiResponse{})
		}

		return apiResponse{}, fmt.Errorf("decode Telegram response: response exceeds %d bytes", maxAPIResponseBytes)
	}

	apiResult, decodeErr := decodeAPIResponse(body)
	if response.StatusCode != http.StatusOK {
		return apiResponse{}, newRejectedError(response, apiResult)
	}
	if decodeErr != nil {
		return apiResponse{}, decodeErr
	}
	if !apiResult.OK {
		return apiResponse{}, newRejectedError(response, apiResult)
	}

	return apiResult, nil
}

func decodeAPIResponse(body []byte) (apiResponse, error) {
	var result apiResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return apiResponse{}, fmt.Errorf("decode Telegram response: %w", err)
	}

	return result, nil
}

func newRejectedError(response *http.Response, result apiResponse) error {
	description := result.Description
	if description == "" {
		description = response.Status
	}
	if description == "" {
		description = fmt.Sprintf("HTTP status %d", response.StatusCode)
	}

	return &rejectedError{
		description: description,
		retryAfter:  time.Duration(result.Parameters.RetryAfter) * time.Second,
	}
}

func requestError(ctx context.Context, operation string, cause error) error {
	safeCause := &safeCauseError{message: operation, cause: cause}
	if contextErr := ctx.Err(); contextErr != nil {
		return errors.Join(ErrRequest, contextErr, safeCause)
	}

	return errors.Join(ErrRequest, safeCause)
}

func hasResult(result json.RawMessage) bool {
	trimmed := bytes.TrimSpace(result)

	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func longPollTimeout(timeout time.Duration) int {
	seconds := int(timeout / time.Second)
	if seconds <= 1 {
		return 0
	}

	return seconds - 1
}

type inputRichMessage struct {
	HTML string `json:"html"`
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

type safeCauseError struct {
	cause   error
	message string
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

func (e *safeCauseError) Error() string {
	return e.message
}

func (e *safeCauseError) Is(target error) bool {
	return errors.Is(e.cause, target)
}
