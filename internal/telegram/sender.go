// Package telegram sends digest messages through the Telegram Bot API.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	telegrambot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// ErrRejected indicates that Telegram returned an unsuccessful API response.
var ErrRejected = errors.New("telegram rejected the message")

// ErrRequest indicates that the Bot API could not be reached.
var ErrRequest = errors.New("telegram request failed")

// RefreshCallbackData identifies digest refresh button presses.
const RefreshCallbackData = "refresh"

const refreshButtonText = "Обновить"

const maxAPIResponseBytes = 1 << 20

var errResponseTooLarge = fmt.Errorf("response exceeds %d bytes", maxAPIResponseBytes)

// Config contains the Telegram Bot API connection settings.
type Config struct {
	APIBase string
	Token   string
	ChatID  string
	Timeout time.Duration
}

// Sender delivers digest messages through the Telegram Bot API.
type Sender struct {
	bot       *telegrambot.Bot
	sdkErrors chan error
	chatID    string
	secrets   []string
}

// NewSender validates the API settings without exposing the bot token.
func NewSender(config Config) (*Sender, error) {
	return newSender(config, &http.Client{Timeout: config.Timeout})
}

func newSender(config Config, client *http.Client) (*Sender, error) {
	serverURL, err := validateConfig(config)
	if err != nil {
		return nil, err
	}

	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Timeout = config.Timeout
	client.Transport = &responseLimitRoundTripper{base: transport}
	sender := &Sender{
		chatID:    config.ChatID,
		secrets:   []string{config.Token, config.APIBase, serverURL},
		sdkErrors: make(chan error, 1),
	}
	sdkBot, err := telegrambot.New(
		config.Token,
		telegrambot.WithServerURL(serverURL),
		telegrambot.WithHTTPClient(config.Timeout, &sdkHTTPClient{client: client}),
		telegrambot.WithSkipGetMe(),
		telegrambot.WithAllowedUpdates(telegrambot.AllowedUpdates{models.AllowedUpdateCallbackQuery}),
		telegrambot.WithErrorsHandler(sender.handleSDKError),
		telegrambot.WithDefaultHandler(ignoreSDKUpdate),
		telegrambot.WithNotAsyncHandlers(),
	)
	if err != nil {
		return nil, &safeCauseError{message: "create Telegram SDK client", cause: err}
	}
	sender.bot = sdkBot

	return sender, nil
}

func validateConfig(config Config) (string, error) {
	endpoint, err := url.Parse(config.APIBase)
	if err != nil {
		return "", &safeCauseError{message: "parse Telegram API URL: invalid URL", cause: err}
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return "", fmt.Errorf("parse Telegram API URL: unsupported scheme %q", endpoint.Scheme)
	}
	if endpoint.Host == "" || config.Token == "" || config.ChatID == "" {
		return "", errors.New("create Telegram sender: API host, token, and chat ID are required")
	}
	if config.Timeout <= 0 {
		return "", errors.New("create Telegram sender: timeout must be positive")
	}

	endpoint.RawQuery = ""
	endpoint.Fragment = ""

	return strings.TrimRight(endpoint.String(), "/"), nil
}

// Send posts one rich HTML digest message, optionally without a notification sound.
func (s *Sender) Send(ctx context.Context, text string, silent bool, attachRefresh bool) error {
	params := &telegrambot.SendRichMessageParams{
		ChatID:              s.chatID,
		RichMessage:         models.InputRichMessage{HTML: text},
		DisableNotification: silent,
	}
	if attachRefresh {
		params.ReplyMarkup = models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{
				{
					{
						Text:         refreshButtonText,
						CallbackData: RefreshCallbackData,
					},
				},
			},
		}
	}

	sdkCtx, call := beginSDKCall(ctx)
	_, err := s.bot.SendRichMessage(sdkCtx, params)

	return s.normalizeSDKCallError(ctx, call, err)
}

func (s *Sender) normalizeSDKCallError(ctx context.Context, call *sdkCall, err error) error {
	unsuccessfulStatus := call.statusCode != 0 &&
		(call.statusCode < http.StatusOK || call.statusCode >= http.StatusMultipleChoices)
	if err == nil && unsuccessfulStatus {
		description := strings.TrimSpace(fmt.Sprintf("%d %s", call.statusCode, http.StatusText(call.statusCode)))

		return &rejectedError{description: description}
	}

	return s.normalizeSDKError(ctx, err)
}

func (s *Sender) normalizeSDKError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return errors.Join(
			ErrRequest,
			contextErr,
			&safeCauseError{message: "call Telegram SDK", cause: err},
		)
	}
	if errors.Is(err, errResponseTooLarge) {
		return fmt.Errorf("decode Telegram response: %w", errResponseTooLarge)
	}

	var rateLimitErr *telegrambot.TooManyRequestsError
	if errors.As(err, &rateLimitErr) {
		retryAfter := time.Duration(rateLimitErr.RetryAfter) * time.Second
		if retryAfter < 0 {
			retryAfter = 0
		}

		return &rejectedError{
			description: s.sanitizeError(rateLimitErr.Message),
			retryAfter:  retryAfter,
		}
	}
	if isSDKRejection(err) {
		return &rejectedError{description: s.sanitizeError(err.Error())}
	}
	if strings.Contains(err.Error(), "error decode response") {
		return &safeCauseError{message: "decode Telegram response", cause: err}
	}

	var requestErr *sdkRequestError
	if errors.As(err, &requestErr) {
		return requestError(ctx, "call Telegram SDK", err)
	}

	return &safeCauseError{message: s.sanitizeError(err.Error()), cause: err}
}

func isSDKRejection(err error) bool {
	var migrateErr *telegrambot.MigrateError
	if errors.As(err, &migrateErr) {
		return true
	}

	known := []error{
		telegrambot.ErrorForbidden,
		telegrambot.ErrorBadRequest,
		telegrambot.ErrorUnauthorized,
		telegrambot.ErrorNotFound,
		telegrambot.ErrorConflict,
	}
	for _, target := range known {
		if errors.Is(err, target) {
			return true
		}
	}

	return strings.Contains(err.Error(), "error response from telegram for method")
}

func (s *Sender) handleSDKError(err error) {
	if !strings.HasPrefix(err.Error(), "error get updates,") {
		return
	}

	select {
	case s.sdkErrors <- err:
	default:
	}
}

func (s *Sender) sanitizeError(message string) string {
	for _, secret := range s.secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "***")
		}
	}

	return message
}

func requestError(ctx context.Context, operation string, cause error) error {
	safeCause := &safeCauseError{message: operation, cause: cause}
	if contextErr := ctx.Err(); contextErr != nil {
		return errors.Join(ErrRequest, contextErr, safeCause)
	}

	return errors.Join(ErrRequest, safeCause)
}

type rejectedError struct {
	description string
	retryAfter  time.Duration
}

type safeCauseError struct {
	cause   error
	message string
}

type responseLimitRoundTripper struct {
	base http.RoundTripper
}

type responseLimitBody struct {
	body      io.ReadCloser
	remaining int64
}

type sdkHTTPClient struct {
	client *http.Client
}

type sdkRequestError struct {
	cause error
}

type sdkCall struct {
	statusCode int
}

type sdkCallContextKey struct{}

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

func (transport *responseLimitRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err != nil || response.Body == nil {
		return response, err
	}
	response.Body = &responseLimitBody{
		body:      response.Body,
		remaining: maxAPIResponseBytes,
	}

	return response, nil
}

func (body *responseLimitBody) Read(buffer []byte) (int, error) {
	if body.remaining == 0 {
		var extra [1]byte
		count, err := body.body.Read(extra[:])
		if count > 0 {
			return 0, errResponseTooLarge
		}

		return 0, wrapResponseReadError(err)
	}
	if int64(len(buffer)) > body.remaining {
		buffer = buffer[:body.remaining]
	}
	count, err := body.body.Read(buffer)
	body.remaining -= int64(count)

	return count, wrapResponseReadError(err)
}

func (body *responseLimitBody) Close() error {
	return body.body.Close()
}

func wrapResponseReadError(err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return err
	}

	return &sdkRequestError{cause: err}
}

func (e *sdkRequestError) Error() string {
	return "Telegram request failed"
}

func (e *sdkRequestError) Is(target error) bool {
	return errors.Is(e.cause, target)
}

func (client *sdkHTTPClient) Do(request *http.Request) (*http.Response, error) {
	//nolint:gosec // Operator-configured API base intentionally supports HTTP(S) test and proxy servers.
	response, err := client.client.Do(request)
	if err != nil {
		return response, &sdkRequestError{cause: err}
	}
	if call, ok := request.Context().Value(sdkCallContextKey{}).(*sdkCall); ok {
		call.statusCode = response.StatusCode
	}

	return response, nil
}

func beginSDKCall(ctx context.Context) (context.Context, *sdkCall) {
	call := &sdkCall{}

	return context.WithValue(ctx, sdkCallContextKey{}, call), call
}
