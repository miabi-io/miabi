// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/netguard"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

const telegramAPIBase = "https://api.telegram.org"

// TelegramSender delivers notifications via the Telegram Bot API.
type TelegramSender struct {
	client *http.Client
}

func NewTelegramSender() *TelegramSender {
	return &TelegramSender{client: netguard.Client(10 * time.Second)}
}

// Send renders the event and posts it to the configured chat.
func (s *TelegramSender) Send(ctx context.Context, ch *models.NotificationChannel, e *models.AppEvent) error {
	token, chatID, err := s.creds(ch)
	if err != nil {
		return err
	}
	return s.sendMessage(ctx, token, chatID, formatMessage(e))
}

// Validate confirms the bot token is valid and the chat is reachable by sending
// a confirmation message.
func (s *TelegramSender) Validate(ctx context.Context, ch *models.NotificationChannel) error {
	token, chatID, err := s.creds(ch)
	if err != nil {
		return err
	}
	return s.sendMessage(ctx, token, chatID, "✅ Miabi test notification — this channel is configured correctly.")
}

func (s *TelegramSender) creds(ch *models.NotificationChannel) (token, chatID string, err error) {
	enc := strings.TrimSpace(ch.Config[models.ConfigBotToken])
	chatID = strings.TrimSpace(ch.Config[models.ConfigChatID])
	if enc == "" {
		return "", "", fmt.Errorf("telegram bot token is not configured")
	}
	if chatID == "" {
		return "", "", fmt.Errorf("telegram chat id is not configured")
	}
	token, err = crypto.Decrypt(enc)
	if err != nil {
		return "", "", fmt.Errorf("decrypt bot token: %w", err)
	}
	return token, chatID, nil
}

func (s *TelegramSender) sendMessage(ctx context.Context, token, chatID, text string) error {
	body, err := json.Marshal(map[string]any{
		"chat_id": chatID,
		"text":    text,
	})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/bot%s/sendMessage", telegramAPIBase, token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		// Surface the API's description (e.g. "chat not found") for the test
		// endpoint without leaking the token (which is only in the URL).
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("telegram API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}
