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
	EventContainerRemoved AppEventType = "container.removed"
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
	EventPipelineAdopted  AppEventType = "pipeline.adopted"
)

// Outcomes, not requests: the audit log records the restart ask, this whether the instance came back.
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

const (
	EventDriftDetected AppEventType = "drift.detected"
	EventDriftResolved AppEventType = "drift.resolved"
)

// Declining to act matters too: an app left alone because its data is gone has to say so.
const (
	EventReconcileRedeploy    AppEventType = "reconcile.redeploy"
	EventReconcileBlocked     AppEventType = "reconcile.blocked"
	EventReconcileBreakerOpen AppEventType = "reconcile.breaker_open"
)

// The matching id column holds the subject and the other stays 0.
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

// Subject-scoped despite the name, which stays because the type is the outbound webhook contract.
type AppEvent struct {
	ID          uint             `json:"id" gorm:"primaryKey"`
	WorkspaceID uint             `json:"workspace_id" gorm:"index;not null"`
	SubjectType EventSubjectType `json:"subject_type" gorm:"not null;default:app"`
	// 0 rather than a pointer because notify.appLabel already treats 0 as "no application".
	ApplicationID uint              `json:"application_id" gorm:"index:idx_event_app_id;not null;default:0"`
	DatabaseID    uint              `json:"database_id,omitempty" gorm:"index:idx_event_db_id;not null;default:0"`
	Type          AppEventType      `json:"type" gorm:"not null"`
	Severity      AppEventSeverity  `json:"severity" gorm:"not null;default:info"`
	Message       string            `json:"message"`
	Metadata      map[string]string `json:"metadata,omitempty" gorm:"serializer:json"`
	ActorID       *uint             `json:"actor_id,omitempty"`
	CreatedAt     time.Time         `json:"created_at" gorm:"index:idx_event_app_id;index:idx_event_db_id"`

	// Populated at delivery time, never persisted; empty when the resource has been deleted.
	ApplicationName string `json:"application_name,omitempty" gorm:"-"`
	ApplicationSlug string `json:"application_slug,omitempty" gorm:"-"`
	DatabaseName    string `json:"database_name,omitempty" gorm:"-"`
}

// Defaults to application so rows written before SubjectType existed keep working.
func (e *AppEvent) Subject() (EventSubjectType, uint) {
	if e.SubjectType == SubjectDatabase {
		return SubjectDatabase, e.DatabaseID
	}
	return SubjectApp, e.ApplicationID
}

// An event without a subject cannot be listed, streamed or trimmed, so the recorder drops it.
func (e *AppEvent) HasSubject() bool {
	_, id := e.Subject()
	return id != 0
}
