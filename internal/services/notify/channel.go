// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"context"
	"fmt"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
)

// ChannelSender delivers a rendered event message over one transport.
type ChannelSender interface {
	// Send delivers the event to the channel. Returning an error signals the
	// worker to retry.
	Send(ctx context.Context, ch *models.NotificationChannel, e *models.AppEvent) error
	// Validate checks the channel's configuration by performing a live probe
	// (used by the test endpoint).
	Validate(ctx context.Context, ch *models.NotificationChannel) error
}

// Registry resolves a sender by channel type.
type Registry struct {
	senders map[models.NotificationChannelType]ChannelSender
}

// NewRegistry builds the default registry with all supported channel types.
func NewRegistry() *Registry {
	return &Registry{
		senders: map[models.NotificationChannelType]ChannelSender{
			models.ChannelTelegram: NewTelegramSender(),
			models.ChannelSlack:    NewSlackSender(),
			models.ChannelDiscord:  NewDiscordSender(),
		},
	}
}

// Sender returns the sender for a channel type, or false if unsupported.
func (r *Registry) Sender(t models.NotificationChannelType) (ChannelSender, bool) {
	s, ok := r.senders[t]
	return s, ok
}

// Send dispatches an event to a channel via its type's sender.
func (r *Registry) Send(ctx context.Context, ch *models.NotificationChannel, e *models.AppEvent) error {
	s, ok := r.Sender(ch.Type)
	if !ok {
		return fmt.Errorf("unsupported channel type %q", ch.Type)
	}
	return s.Send(ctx, ch, e)
}

// Validate probes a channel's configuration via its type's sender.
func (r *Registry) Validate(ctx context.Context, ch *models.NotificationChannel) error {
	s, ok := r.Sender(ch.Type)
	if !ok {
		return fmt.Errorf("unsupported channel type %q", ch.Type)
	}
	return s.Validate(ctx, ch)
}

// formatMessage renders a human-readable notification body from an event. The
// first line leads with the resource name so a glance answers "what, and to
// which app"; the detail and timestamp follow.
func formatMessage(e *models.AppEvent) string {
	var b strings.Builder
	b.WriteString(eventEmoji(e.Type))
	b.WriteString(" ")
	if subject := subjectLabel(e); subject != "" {
		fmt.Fprintf(&b, "%s — %s", subject, eventTitle(e.Type))
	} else {
		b.WriteString(eventTitle(e.Type))
	}
	if e.Message != "" {
		fmt.Fprintf(&b, "\n%s", e.Message)
	}
	if !e.CreatedAt.IsZero() {
		fmt.Fprintf(&b, "\n%s", e.CreatedAt.UTC().Format("2006-01-02 15:04:05 UTC"))
	}
	return b.String()
}

// subjectLabel returns the best available human label for the event's subject: its name when
// enrichment populated it, otherwise a "#id" fallback (e.g. the resource was deleted before
// delivery), or empty when the event names no subject.
func subjectLabel(e *models.AppEvent) string {
	subject, id := e.Subject()
	if id == 0 {
		return ""
	}
	name := e.ApplicationName
	if subject == models.SubjectDatabase {
		name = e.DatabaseName
	}
	if name != "" {
		return name
	}
	return fmt.Sprintf("#%d", id)
}

func eventEmoji(t models.AppEventType) string {
	switch t {
	case models.EventDeploySucceeded, models.EventContainerStarted:
		return "✅"
	case models.EventDeployFailed, models.EventContainerDied, models.EventContainerOOM,
		models.EventDatabaseProvisionFailed, models.EventDatabaseUpgradeFailed,
		models.EventDatabaseBackupFailed, models.EventDatabaseRestoreFailed:
		return "❌"
	case models.EventDatabaseProvisioned, models.EventDatabaseStarted,
		models.EventDatabaseBackupSucceeded, models.EventDatabaseRestoreSucceeded:
		return "✅"
	case models.EventDatabaseStopped:
		return "🛑"
	case models.EventDatabaseUpgraded:
		return "⬆️"
	case models.EventDeployStarted:
		return "🚀"
	case models.EventContainerStopped:
		return "🛑"
	default:
		return "ℹ️"
	}
}

func eventTitle(t models.AppEventType) string {
	switch t {
	case models.EventDeployStarted:
		return "Deployment started"
	case models.EventDeploySucceeded:
		return "Deployment succeeded"
	case models.EventDeployFailed:
		return "Deployment failed"
	case models.EventContainerStarted:
		return "Container started"
	case models.EventContainerStopped:
		return "Container stopped"
	case models.EventContainerDied:
		return "Container exited"
	case models.EventContainerOOM:
		return "Container out of memory"
	case models.EventDatabaseProvisioned:
		return "Database provisioned"
	case models.EventDatabaseProvisionFailed:
		return "Database provisioning failed"
	case models.EventDatabaseStarted:
		return "Database started"
	case models.EventDatabaseStopped:
		return "Database stopped"
	case models.EventDatabaseRestarted:
		return "Database restarted"
	case models.EventDatabaseUpgraded:
		return "Database upgraded"
	case models.EventDatabaseUpgradeFailed:
		return "Database upgrade failed"
	case models.EventDatabaseBackupSucceeded:
		return "Backup completed"
	case models.EventDatabaseBackupFailed:
		return "Backup failed"
	case models.EventDatabaseRestoreSucceeded:
		return "Restore completed"
	case models.EventDatabaseRestoreFailed:
		return "Restore failed"
	default:
		return string(t)
	}
}
