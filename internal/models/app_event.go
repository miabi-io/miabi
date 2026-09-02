// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// AppEventType is a coarse classification of an application event.
type AppEventType string

const (
	EventDeployStarted    AppEventType = "deploy.started"
	EventDeploySucceeded  AppEventType = "deploy.succeeded"
	EventDeployFailed     AppEventType = "deploy.failed"
	EventRollbackStarted  AppEventType = "rollback.started"
	EventReleaseActivated AppEventType = "release.activated"
	EventContainerStarted AppEventType = "container.started"
	EventContainerStopped AppEventType = "container.stopped"
	EventContainerDied    AppEventType = "container.died"
	EventContainerOOM     AppEventType = "container.oom"
	EventContainerHealth  AppEventType = "container.health"
	EventEnvUpdated       AppEventType = "env.updated"
	EventDomainAttached   AppEventType = "domain.attached"
	EventDomainVerified   AppEventType = "domain.verified"
	EventVolumeAttached   AppEventType = "volume.attached"
	EventVolumeDetached   AppEventType = "volume.detached"
	EventConfigAttached   AppEventType = "config.attached"
	EventConfigDetached   AppEventType = "config.detached"
	EventSettingsUpdated  AppEventType = "settings.updated"
	EventAppCreated       AppEventType = "app.created"
	EventAppDeleted       AppEventType = "app.deleted"
	// EventPipelineAdopted records the outcome of reading a git app's repository
	// for pipeline-as-code: adopted, or why it fell back to a direct build.
	EventPipelineAdopted AppEventType = "pipeline.adopted"
)

// AppEventSeverity colors an event in the UI.
type AppEventSeverity string

const (
	SeverityInfo    AppEventSeverity = "info"
	SeverityWarning AppEventSeverity = "warning"
	SeverityError   AppEventSeverity = "error"
)

// AppEvent is a timeline entry for an application: lifecycle transitions, runtime container
// events (start/stop/crash/health), and configuration changes. Distinct from AuditLog (user
// mutations, admin-scoped) and from deployment build logs.
type AppEvent struct {
	ID            uint              `json:"id" gorm:"primaryKey"`
	WorkspaceID   uint              `json:"workspace_id" gorm:"index;not null"`
	ApplicationID uint              `json:"application_id" gorm:"index:idx_event_app_id;not null"`
	Type          AppEventType      `json:"type" gorm:"not null"`
	Severity      AppEventSeverity  `json:"severity" gorm:"not null;default:info"`
	Message       string            `json:"message"`
	Metadata      map[string]string `json:"metadata,omitempty" gorm:"serializer:json"`
	ActorID       *uint             `json:"actor_id,omitempty"`
	CreatedAt     time.Time         `json:"created_at" gorm:"index:idx_event_app_id"`

	// Display fields are populated at delivery time (notifications, webhooks) and
	// are never persisted. They let a renderer name the resource instead of
	// showing a bare numeric id. Empty when the application has been deleted.
	ApplicationName string `json:"application_name,omitempty" gorm:"-"`
	ApplicationSlug string `json:"application_slug,omitempty" gorm:"-"`
}
