// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"sync"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/alerting"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// backupAlerter bridges backup outcomes to alerting, keeping backup.Service decoupled.
type backupAlerter struct{ e *alerting.Engine }

func (a backupAlerter) BackupFailed(ws, dbID uint, name, errMsg string) {
	title := "Database backup failed"
	if name != "" {
		title += " — " + name
	}
	a.e.Emit(alerting.Signal{
		WorkspaceID: ws, Kind: "backup_failed", SubjectType: "database",
		SubjectRef: fmt.Sprintf("database:%d", dbID), SubjectLink: fmt.Sprintf("/databases/%d", dbID),
		Severity: models.AlertCritical, Title: title, Body: errMsg,
	})
}

func (a backupAlerter) BackupSucceeded(ws, dbID uint) {
	a.e.Emit(alerting.Signal{
		WorkspaceID: ws, Kind: "backup_ok", Resolve: true,
		SubjectRef: fmt.Sprintf("database:%d", dbID),
	})
}

func (a backupAlerter) BackupSetFailed(ws, instanceID uint, instanceName, ref, errMsg string) {
	title := "Recovery point failed"
	switch {
	case instanceName != "":
		title += " — " + instanceName
	case ref != "":
		title += " — " + ref
	}
	body := errMsg
	if ref != "" && instanceName != "" {
		body = ref + ": " + errMsg
	}
	a.e.Emit(alerting.Signal{
		WorkspaceID: ws, Kind: "backup_set_failed", SubjectType: "database",
		SubjectRef: fmt.Sprintf("instance:%d", instanceID), SubjectLink: fmt.Sprintf("/databases/%d", instanceID),
		Severity: models.AlertCritical, Title: title, Body: body,
	})
}

func (a backupAlerter) BackupSetSucceeded(ws, instanceID uint) {
	a.e.Emit(alerting.Signal{
		WorkspaceID: ws, Kind: "backup_set_ok", Resolve: true,
		SubjectRef: fmt.Sprintf("instance:%d", instanceID),
	})
}

// volumeAlerter raises backup_failed for a volume recovery point and resolves it on the next good one.
type volumeAlerter struct{ e *alerting.Engine }

func (a volumeAlerter) VolumeBackupFailed(ws, volumeID uint, volumeName, ref, errMsg string) {
	title := "Volume recovery point failed"
	if volumeName != "" {
		title += " — " + volumeName
	}
	body := errMsg
	if ref != "" {
		body = ref + ": " + errMsg
	}
	a.e.Emit(alerting.Signal{
		WorkspaceID: ws, Kind: "backup_failed", SubjectType: "volume",
		SubjectRef: fmt.Sprintf("volume:%d", volumeID), SubjectLink: fmt.Sprintf("/volumes/%d", volumeID),
		Severity: models.AlertCritical, Title: title, Body: body,
	})
}

func (a volumeAlerter) VolumeBackupSucceeded(ws, volumeID uint) {
	a.e.Emit(alerting.Signal{
		WorkspaceID: ws, Kind: "backup_ok", Resolve: true,
		SubjectRef: fmt.Sprintf("volume:%d", volumeID),
	})
}

type backupReporter struct{ n *alerting.WorkspaceNotifier }

func (r backupReporter) ScheduledBackupFinished(ws uint, rep backup.BackupReport) {
	item := models.Notification{
		Category:    models.CategoryDatabase,
		SubjectLink: rep.Link,
		ActionText:  "View backups",
	}
	what := "Backup"
	if rep.Ref != "" {
		what = "Recovery point"
	}
	switch {
	case rep.OK && rep.Databases > 0:
		item.Severity = models.AlertInfo
		item.Title = fmt.Sprintf("%s completed — %s", what, rep.Subject)
		item.Body = fmt.Sprintf("%s covered %s and stored %s.",
			rep.Ref, pluralDatabases(rep.Databases), humanBytes(rep.SizeBytes))
	case rep.OK:
		item.Severity = models.AlertInfo
		item.Title = fmt.Sprintf("%s completed — %s", what, rep.Subject)
		item.Body = fmt.Sprintf("The scheduled backup stored %s.", humanBytes(rep.SizeBytes))
	default:

		item.Severity = models.AlertWarning
		item.Title = fmt.Sprintf("%s failed — %s", what, rep.Subject)
		item.Body = rep.Err
		if rep.Ref != "" {
			item.Body = rep.Ref + ": " + rep.Err
		}
	}
	// Developer and up, matching who receives the backup_failed alert: a viewer cannot act on either.
	if err := r.n.NotifyWorkspace(ws, models.WorkspaceRoleDeveloper, item); err != nil {
		logger.Warn("could not report a scheduled backup to the workspace inbox", "workspace", ws, "error", err)
	}
}

func pluralDatabases(n int) string {
	if n == 1 {
		return "1 database"
	}
	return fmt.Sprintf("%d databases", n)
}

// humanBytes renders a size for an inbox line. Deliberately coarse: the exact byte count belongs on
// the backup row, not in a sentence.
func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	val, exp := float64(b), 0
	for val >= unit && exp < 4 {
		val /= unit
		exp++
	}
	return fmt.Sprintf("%.1f %sB", val, [...]string{"", "Ki", "Mi", "Gi", "Ti"}[exp])
}

// quotaScanner implements alerting.QuotaLister over plan quotas and per-workspace counts.
type quotaScanner struct {
	ws   *repositories.WorkspaceRepository
	q    *quota.Service
	apps *repositories.ApplicationRepository
	vols *repositories.VolumeRepository
	dbs  *repositories.DatabaseRepository
}

func (s quotaScanner) NearQuota(threshold float64) ([]alerting.QuotaBreach, error) {
	workspaces, err := s.ws.ListAll()
	if err != nil {
		return nil, err
	}
	var out []alerting.QuotaBreach
	for i := range workspaces {
		w := &workspaces[i]
		lim := s.q.EffectiveLimits(w.ID)
		add := func(resource string, used int64, max int) {
			if max > 0 && float64(used)/float64(max) >= threshold {
				out = append(out, alerting.QuotaBreach{WorkspaceID: w.ID, Resource: resource, Used: int(used), Limit: max})
			}
		}
		if n, err := s.apps.CountByWorkspace(w.ID); err == nil {
			add("apps", n, lim.MaxApps)
		}
		if n, err := s.vols.CountByWorkspace(w.ID); err == nil {
			add("volumes", n, lim.MaxVolumes)
		}
		if n, err := s.dbs.CountInstancesByWorkspace(w.ID); err == nil {
			add("database instances", n, lim.MaxDatabaseInstances)
		}
	}
	return out, nil
}

// platformAlerter bridges node/runner online/offline hooks to alerts.
type platformAlerter struct {
	e  *alerting.Engine
	ws *repositories.WorkspaceRepository

	mu      sync.Mutex
	sysWsID uint
}

func (n *platformAlerter) systemWorkspace() uint {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.sysWsID != 0 {
		return n.sysWsID
	}
	if w, err := n.ws.FindSystem(); err == nil && w != nil {
		n.sysWsID = w.ID
	}
	return n.sysWsID
}

// NodeStatus emits node_offline / node_online (auto-resolving) platform signals.
func (n *platformAlerter) NodeStatus(nodeID uint, name string, online bool) {
	wsID := n.systemWorkspace()
	if wsID == 0 {
		return
	}
	ref := fmt.Sprintf("node:%d", nodeID)
	if online {
		n.e.Emit(alerting.Signal{WorkspaceID: wsID, Kind: "node_online", Resolve: true, SubjectRef: ref, Platform: true})
		return
	}
	n.e.Emit(alerting.Signal{
		WorkspaceID: wsID, Kind: "node_offline", SubjectType: "node", SubjectRef: ref,
		SubjectLink: fmt.Sprintf("/admin/nodes/%d", nodeID), Severity: models.AlertCritical,
		Title: "Node offline — " + name, Platform: true,
		Body: "The node's agent tunnel dropped; workloads scheduled on it are unreachable.",
	})
}
