// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package webhook manages workspace webhooks and the signing/delivery of their
// JSON payloads. Signing secrets are encrypted at rest.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/netguard"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrNotFound     = errors.New("webhook not found")
	ErrURLRequired  = errors.New("webhook url is required")
	ErrURLInvalid   = errors.New("webhook url must be a valid http(s) url")
	ErrURLBlocked   = errors.New("webhook url targets a disallowed (internal) address")
	ErrInvalidEvent = errors.New("unknown or non-notifiable event")
)

// SignatureHeader carries the HMAC-SHA256 signature of the request body.
const SignatureHeader = "X-Miabi-Signature"

// EventTest is the synthetic event used by the test endpoint.
const EventTest = "webhook.test"

// Payload is the JSON body POSTed to a webhook endpoint.
type Payload struct {
	Event           string            `json:"event"`
	WorkspaceID     uint              `json:"workspace_id"`
	ApplicationID   uint              `json:"application_id,omitempty"`
	ApplicationName string            `json:"application_name,omitempty"`
	ApplicationSlug string            `json:"application_slug,omitempty"`
	Severity        string            `json:"severity,omitempty"`
	Message         string            `json:"message,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	Timestamp       string            `json:"timestamp"`
}

// Enqueuer schedules an asynchronous webhook delivery. Implemented by the
// worker producer; an interface here avoids a webhook->worker import cycle.
type Enqueuer interface {
	EnqueueWebhookDeliver(webhookID, eventID uint) error
}

// Service manages webhook configuration and synchronous test delivery.
type Service struct {
	repo       *repositories.WebhookRepository
	deliveries *repositories.WebhookDeliveryRepository
	enqueuer   Enqueuer
	client     *http.Client
}

func NewService(repo *repositories.WebhookRepository, deliveries *repositories.WebhookDeliveryRepository, enqueuer Enqueuer) *Service {
	return &Service{repo: repo, deliveries: deliveries, enqueuer: enqueuer, client: netguard.Client(10 * time.Second)}
}

// Input describes a webhook to create or update.
type Input struct {
	Name    string
	URL     string
	Events  []string
	Headers map[string]string
	Enabled bool
}

func (s *Service) Create(workspaceID uint, in Input) (*models.Webhook, error) {
	if err := validateURL(in.URL); err != nil {
		return nil, err
	}
	if err := validateEvents(in.Events); err != nil {
		return nil, err
	}
	secret, err := GenerateSecret()
	if err != nil {
		return nil, err
	}
	enc, err := crypto.EncryptWS(workspaceID, secret)
	if err != nil {
		return nil, err
	}
	w := &models.Webhook{
		WorkspaceID: workspaceID,
		Name:        in.Name,
		URL:         in.URL,
		Secret:      enc,
		Events:      in.Events,
		Headers:     in.Headers,
		Enabled:     in.Enabled,
	}
	if err := s.repo.Create(w); err != nil {
		return nil, err
	}
	out := strip(w)
	out.PlainSecret = secret // returned exactly once
	return out, nil
}

func (s *Service) Update(workspaceID, id uint, in Input) (*models.Webhook, error) {
	w, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	if in.URL != "" {
		if err := validateURL(in.URL); err != nil {
			return nil, err
		}
		w.URL = in.URL
	}
	if in.Name != "" {
		w.Name = in.Name
	}
	if in.Events != nil {
		if err := validateEvents(in.Events); err != nil {
			return nil, err
		}
		w.Events = in.Events
	}
	if in.Headers != nil {
		w.Headers = in.Headers
	}
	w.Enabled = in.Enabled
	if err := s.repo.Update(w); err != nil {
		return nil, err
	}
	return strip(w), nil
}

// Redeliver re-sends a past delivery's source event to its webhook (async). If
// the delivery was a test (no source event), a fresh test delivery is sent.
func (s *Service) Redeliver(workspaceID, webhookID, deliveryID uint) error {
	w, err := s.repo.FindInWorkspace(workspaceID, webhookID)
	if err != nil {
		return ErrNotFound
	}
	d, err := s.deliveries.FindInWorkspace(workspaceID, deliveryID)
	if err != nil || d.WebhookID != w.ID {
		return ErrNotFound
	}
	if d.EventID == 0 {
		return s.Test(context.Background(), workspaceID, webhookID)
	}
	if s.enqueuer == nil {
		return errors.New("delivery queue unavailable")
	}
	return s.enqueuer.EnqueueWebhookDeliver(w.ID, d.EventID)
}

func (s *Service) Get(workspaceID, id uint) (*models.Webhook, error) {
	w, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return strip(w), nil
}

func (s *Service) List(workspaceID uint) ([]models.Webhook, error) {
	ws, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range ws {
		strip(&ws[i])
	}
	return ws, nil
}

func (s *Service) Delete(workspaceID, id uint) error {
	w, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	return s.repo.Delete(w.ID)
}

// Deliveries returns a webhook's recent delivery log.
func (s *Service) Deliveries(workspaceID, id uint, limit int) ([]models.WebhookDelivery, error) {
	if _, err := s.repo.FindInWorkspace(workspaceID, id); err != nil {
		return nil, ErrNotFound
	}
	return s.deliveries.ListForWebhook(workspaceID, id, limit)
}

// Test synchronously delivers a signed test payload and records the outcome,
// returning any delivery error to the caller for immediate feedback.
func (s *Service) Test(ctx context.Context, workspaceID, id uint) error {
	w, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	secret, err := crypto.Decrypt(w.Secret)
	if err != nil {
		return fmt.Errorf("decrypt secret: %w", err)
	}
	body, err := json.Marshal(Payload{
		Event:       EventTest,
		WorkspaceID: workspaceID,
		Message:     "Test delivery from Miabi",
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	status, derr := Deliver(ctx, s.client, w.URL, secret, w.Headers, body)
	delivery := &models.WebhookDelivery{
		WebhookID:      w.ID,
		WorkspaceID:    workspaceID,
		Event:          EventTest,
		HTTPStatusCode: status,
		Attempt:        1,
	}
	if derr != nil || status >= 400 {
		delivery.Status = models.WebhookDeliveryFailed
		if derr != nil {
			delivery.ErrorMessage = derr.Error()
		} else {
			delivery.ErrorMessage = fmt.Sprintf("HTTP %d", status)
		}
		_ = s.deliveries.Create(delivery)
		if derr != nil {
			return derr
		}
		return fmt.Errorf("endpoint returned status %d", status)
	}
	delivery.Status = models.WebhookDeliverySuccess
	_ = s.deliveries.Create(delivery)
	return nil
}

// BuildPayload serializes an application event into a webhook payload.
func BuildPayload(e *models.AppEvent) ([]byte, error) {
	return json.Marshal(Payload{
		Event:           string(e.Type),
		WorkspaceID:     e.WorkspaceID,
		ApplicationID:   e.ApplicationID,
		ApplicationName: e.ApplicationName,
		ApplicationSlug: e.ApplicationSlug,
		Severity:        string(e.Severity),
		Message:         e.Message,
		Metadata:        e.Metadata,
		Timestamp:       e.CreatedAt.UTC().Format(time.RFC3339),
	})
}

// Sign returns the hex HMAC-SHA256 of body keyed by secret.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Deliver POSTs a signed JSON body to url and returns the HTTP status code. A transport error returns (0,
// err); a non-2xx/3xx status is reported via the returned code, and the caller decides whether to retry.
// User-supplied headers are applied first; Content-Type, User-Agent and the signature cannot be overridden.
func Deliver(ctx context.Context, client *http.Client, url, secret string, headers map[string]string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Miabi-Webhook/1.0")
	if secret != "" {
		req.Header.Set(SignatureHeader, "sha256="+Sign(secret, body))
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// GenerateSecret returns a 64-char hex string from 32 random bytes.
func GenerateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func strip(w *models.Webhook) *models.Webhook {
	w.HasSecret = w.Secret != ""
	w.Secret = ""
	return w
}

func validateURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ErrURLRequired
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ErrURLInvalid
	}
	// Reject SSRF-prone targets (loopback, link-local/metadata, and — unless
	// allowed — private ranges). DNS names are re-checked at delivery time.
	if err := netguard.ValidateURL(raw); err != nil {
		var blocked *netguard.ErrBlocked
		if errors.As(err, &blocked) {
			return ErrURLBlocked
		}
		return ErrURLInvalid
	}
	return nil
}

func validateEvents(events []string) error {
	for _, e := range events {
		if !models.IsNotifiable(models.AppEventType(e)) {
			return ErrInvalidEvent
		}
	}
	return nil
}
