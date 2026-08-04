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

var (
	ErrRejected = errors.New("telegram rejected the message")
	ErrRequest  = errors.New("telegram request failed")
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
	client   *http.Client
	endpoint string
	chatID   string
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

	endpoint.Path = path.Join(endpoint.Path, "bot"+config.Token, "sendMessage")
	return &Sender{
		client:   &http.Client{Timeout: config.Timeout},
		endpoint: endpoint.String(),
		chatID:   config.ChatID,
	}, nil
}

// Send posts one HTML-formatted digest message.
func (s *Sender) Send(ctx context.Context, text string) error {
	payload := struct {
		ChatID             string             `json:"chat_id"`
		Text               string             `json:"text"`
		ParseMode          string             `json:"parse_mode"`
		LinkPreviewOptions linkPreviewOptions `json:"link_preview_options"`
	}{
		ChatID:    s.chatID,
		Text:      text,
		ParseMode: "HTML",
		LinkPreviewOptions: linkPreviewOptions{
			Disabled: true,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode Telegram request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create Telegram request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := s.client.Do(request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return fmt.Errorf("send Telegram message: %w", contextErr)
		}
		return ErrRequest
	}
	defer response.Body.Close()

	var result apiResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return fmt.Errorf("decode Telegram response: %w", err)
	}
	if response.StatusCode != http.StatusOK || !result.OK {
		if result.Description == "" {
			result.Description = response.Status
		}
		return fmt.Errorf("%w: %s", ErrRejected, result.Description)
	}

	return nil
}

type linkPreviewOptions struct {
	Disabled bool `json:"is_disabled"`
}

type apiResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}
