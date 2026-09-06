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

// Database instance event types. These record outcomes and unattended activity, which is what
// separates them from AuditLog: the audit log records that a user asked for a restart, this
// records whether the instance came back.
const (
	EventDatabaseProvisioned      AppEventType = "database.provisioned"
	EventDatabaseProvisionFailed  AppEventType = "database.provision_failed"
	EventDatabaseStarted          AppEventType = "database.started"
	EventDatabaseStopped          AppEventType = "database.stopped"
	EventDatabaseRestarted        AppEventType = "database.restarted"
	EventDatabaseUpgraded         AppEventType = "database.upgraded"
	EventDatabaseUpgradeFailed    AppEventType = "database.upgrade_failed"
	EventDatabaseBackupSucceeded  AppEventType = "backup.succeeded"
	EventDatabaseBackupFailed     AppEventType = "backup.failed"
	EventDatabaseRestoreSucceeded AppEventType = "restore.succeeded"
	EventDatabaseRestoreFailed    AppEventType = "restore.failed"
)

// EventSubjectType names the kind of resource a timeline event is about. An event carries
// exactly one subject; the matching id column holds it and the other stays 0.
type EventSubjectType string

const (
	SubjectApp      EventSubjectType = "app"
	SubjectDatabase EventSubjectType = "database"
)

// AppEventSeverity colors an event in the UI.
type AppEventSeverity string

const (
	SeverityInfo    AppEventSeverity = "info"
	SeverityWarning AppEventSeverity = "warning"
	SeverityError   AppEventSeverity = "error"
)

// AppEvent is a timeline entry for a resource: lifecycle transitions, runtime container
// events (start/stop/crash/health), and configuration changes. Distinct from AuditLog (user
// mutations, admin-scoped) and from deployment build logs.
//
// Despite the name it is subject-scoped: SubjectType says which of ApplicationID / DatabaseID
// is set. The name stays because the type is the outbound webhook contract.
type AppEvent struct {
	ID          uint             `json:"id" gorm:"primaryKey"`
	WorkspaceID uint             `json:"workspace_id" gorm:"index;not null"`
	SubjectType EventSubjectType `json:"subject_type" gorm:"not null;default:app"`
	// ApplicationID is 0 on a non-app event. 0 rather than a nullable pointer because the
	// delivery layer already reads it that way (notify.appLabel treats 0 as "no application").
	ApplicationID uint              `json:"application_id" gorm:"index:idx_event_app_id;not null;default:0"`
	DatabaseID    uint              `json:"database_id,omitempty" gorm:"index:idx_event_db_id;not null;default:0"`
	Type          AppEventType      `json:"type" gorm:"not null"`
	Severity      AppEventSeverity  `json:"severity" gorm:"not null;default:info"`
	Message       string            `json:"message"`
	Metadata      map[string]string `json:"metadata,omitempty" gorm:"serializer:json"`
	ActorID       *uint             `json:"actor_id,omitempty"`
	CreatedAt     time.Time         `json:"created_at" gorm:"index:idx_event_app_id;index:idx_event_db_id"`

	// Display fields are populated at delivery time (notifications, webhooks) and
	// are never persisted. They let a renderer name the resource instead of
	// showing a bare numeric id. Empty when the resource has been deleted.
	ApplicationName string `json:"application_name,omitempty" gorm:"-"`
	ApplicationSlug string `json:"application_slug,omitempty" gorm:"-"`
	DatabaseName    string `json:"database_name,omitempty" gorm:"-"`
}

// Subject returns the event's subject type and id, defaulting to the application subject so
// rows written before SubjectType existed keep behaving as application events.
func (e *AppEvent) Subject() (EventSubjectType, uint) {
	if e.SubjectType == SubjectDatabase {
		return SubjectDatabase, e.DatabaseID
	}
	return SubjectApp, e.ApplicationID
}

// HasSubject reports whether the event names a resource. An event without one cannot be
// listed, streamed or trimmed, so the recorder drops it.
func (e *AppEvent) HasSubject() bool {
	_, id := e.Subject()
	return id != 0
}
