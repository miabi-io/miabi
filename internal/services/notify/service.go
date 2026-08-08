// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"context"
	"errors"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/netguard"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrNotFound           = errors.New("notification channel not found")
	ErrNameRequired       = errors.New("channel name is required")
	ErrUnsupportedType    = errors.New("unsupported channel type")
	ErrTokenRequired      = errors.New("telegram bot token is required")
	ErrChatIDRequired     = errors.New("telegram chat id is required")
	ErrWebhookURLRequired = errors.New("webhook url is required")
	ErrInvalidEvent       = errors.New("unknown or non-notifiable event")
)

// supportedTypes lists the channel types the service accepts.
var supportedTypes = map[models.NotificationChannelType]bool{
	models.ChannelTelegram: true,
	models.ChannelSlack:    true,
	models.ChannelDiscord:  true,
}

// secretConfigKeys are masked in API responses.
var secretConfigKeys = []string{models.ConfigBotToken, models.ConfigWebhookURL}

const maskedToken = "********"

// Service manages notification-channel configuration and on-demand testing.
type Service struct {
	repo     *repositories.NotificationChannelRepository
	registry *Registry
}

func NewService(repo *repositories.NotificationChannelRepository, registry *Registry) *Service {
	return &Service{repo: repo, registry: registry}
}

// Input describes a channel to create or update. Secret values are plaintext;
// they are encrypted before storage and, on update, an empty value leaves the
// stored secret unchanged.
type Input struct {
	Type    models.NotificationChannelType
	Name    string
	Events  []string
	Enabled bool
	// Telegram.
	BotToken string
	ChatID   string
	// Slack / Discord.
	WebhookURL string
}

func (s *Service) Create(workspaceID uint, in Input) (*models.NotificationChannel, error) {
	if !supportedTypes[in.Type] {
		return nil, ErrUnsupportedType
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, ErrNameRequired
	}
	if err := validateEvents(in.Events); err != nil {
		return nil, err
	}
	cfg, err := s.buildConfig(workspaceID, in.Type, in, map[string]string{})
	if err != nil {
		return nil, err
	}
	ch := &models.NotificationChannel{
		WorkspaceID: workspaceID,
		Type:        in.Type,
		Name:        in.Name,
		Config:      cfg,
		Events:      in.Events,
		Enabled:     in.Enabled,
	}
	if err := s.repo.Create(ch); err != nil {
		return nil, err
	}
	return mask(ch), nil
}

func (s *Service) Update(workspaceID, id uint, in Input) (*models.NotificationChannel, error) {
	ch, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	if ch.Config == nil {
		ch.Config = map[string]string{}
	}
	if in.Name != "" {
		ch.Name = in.Name
	}
	if in.Events != nil {
		if err := validateEvents(in.Events); err != nil {
			return nil, err
		}
		ch.Events = in.Events
	}
	ch.Enabled = in.Enabled
	// Merge secrets/settings into the existing config (empty secret = keep).
	cfg, err := s.buildConfig(workspaceID, ch.Type, in, ch.Config)
	if err != nil {
		return nil, err
	}
	ch.Config = cfg
	if err := s.repo.Update(ch); err != nil {
		return nil, err
	}
	return mask(ch), nil
}

// buildConfig validates per-type fields and merges them into base, encrypting
// secret values. On create, base is empty and required fields are enforced; on
// update, an empty secret leaves the existing value in place.
func (s *Service) buildConfig(workspaceID uint, t models.NotificationChannelType, in Input, base map[string]string) (map[string]string, error) {
	cfg := map[string]string{}
	for k, v := range base {
		cfg[k] = v
	}
	creating := len(base) == 0
	switch t {
	case models.ChannelTelegram:
		if tok := strings.TrimSpace(in.BotToken); tok != "" {
			enc, err := crypto.EncryptWS(workspaceID, tok)
			if err != nil {
				return nil, err
			}
			cfg[models.ConfigBotToken] = enc
		} else if creating {
			return nil, ErrTokenRequired
		}
		if cid := strings.TrimSpace(in.ChatID); cid != "" {
			cfg[models.ConfigChatID] = cid
		} else if creating {
			return nil, ErrChatIDRequired
		}
	case models.ChannelSlack, models.ChannelDiscord:
		if u := strings.TrimSpace(in.WebhookURL); u != "" {
			if err := netguard.ValidateURL(u); err != nil {
				return nil, ErrWebhookURLRequired
			}
			enc, err := crypto.EncryptWS(workspaceID, u)
			if err != nil {
				return nil, err
			}
			cfg[models.ConfigWebhookURL] = enc
		} else if creating {
			return nil, ErrWebhookURLRequired
		}
	default:
		return nil, ErrUnsupportedType
	}
	return cfg, nil
}

func (s *Service) Get(workspaceID, id uint) (*models.NotificationChannel, error) {
	ch, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return mask(ch), nil
}

func (s *Service) List(workspaceID uint) ([]models.NotificationChannel, error) {
	chs, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range chs {
		mask(&chs[i])
	}
	return chs, nil
}

func (s *Service) Delete(workspaceID, id uint) error {
	ch, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	return s.repo.Delete(ch.ID)
}

// Test sends a confirmation message to the channel using its stored config.
func (s *Service) Test(ctx context.Context, workspaceID, id uint) error {
	ch, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	return s.registry.Validate(ctx, ch)
}

// mask clears secret config values for safe responses, leaving a presence
// indicator in place of the stored token.
func mask(ch *models.NotificationChannel) *models.NotificationChannel {
	if ch.Config != nil {
		for _, k := range secretConfigKeys {
			if _, ok := ch.Config[k]; ok {
				ch.Config[k] = maskedToken
			}
		}
	}
	return ch
}

// validateEvents rejects unknown or non-notifiable event types.
func validateEvents(events []string) error {
	for _, e := range events {
		if !models.IsNotifiable(models.AppEventType(e)) {
			return ErrInvalidEvent
		}
	}
	return nil
}
