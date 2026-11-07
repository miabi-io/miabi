// Copyright (c) 2026 Jonas Kaninda.
//
// This file is part of Miabi.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
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

// Slack and Discord differ only in the JSON field carrying the text; the webhook URL is a secret, stored encrypted.
type webhookChannelSender struct {
	field  string // JSON body field for the message ("text" for Slack, "content" for Discord)
	label  string // human label for errors
	client *http.Client
}

// NewSlackSender posts to a Slack incoming webhook ({"text": ...}).
func NewSlackSender() *webhookChannelSender {
	return &webhookChannelSender{field: "text", label: "Slack", client: netguard.Client(10 * time.Second)}
}

// NewDiscordSender posts to a Discord webhook ({"content": ...}).
func NewDiscordSender() *webhookChannelSender {
	return &webhookChannelSender{field: "content", label: "Discord", client: netguard.Client(10 * time.Second)}
}

func (s *webhookChannelSender) Send(ctx context.Context, ch *models.NotificationChannel, e *models.AppEvent) error {
	url, err := s.url(ch)
	if err != nil {
		return err
	}
	return s.post(ctx, url, formatMessage(e))
}

func (s *webhookChannelSender) Validate(ctx context.Context, ch *models.NotificationChannel) error {
	url, err := s.url(ch)
	if err != nil {
		return err
	}
	return s.post(ctx, url, "✅ Miabi test notification — this channel is configured correctly.")
}

func (s *webhookChannelSender) url(ch *models.NotificationChannel) (string, error) {
	enc := strings.TrimSpace(ch.Config[models.ConfigWebhookURL])
	if enc == "" {
		return "", fmt.Errorf("%s webhook url is not configured", s.label)
	}
	url, err := crypto.Decrypt(enc)
	if err != nil {
		return "", fmt.Errorf("decrypt webhook url: %w", err)
	}
	return url, nil
}

func (s *webhookChannelSender) post(ctx context.Context, url, text string) error {
	body, err := json.Marshal(map[string]string{s.field: text})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s request: %w", s.label, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("%s returned %d: %s", s.label, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}
