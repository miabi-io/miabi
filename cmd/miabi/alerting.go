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

package main

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/alerting"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/services/registryserver"
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

// quotaScanner implements alerting.QuotaLister over plan quotas and per-workspace counts.
type quotaScanner struct {
	ws   *repositories.WorkspaceRepository
	q    *quota.Service
	apps *repositories.ApplicationRepository
	vols *repositories.VolumeRepository
	dbs  *repositories.DatabaseRepository
	// registry is set once the routes build it, after the scan loop has started.
	registry *atomic.Pointer[registryserver.Service]
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
		if b, ok := s.registryBreach(w.ID, threshold); ok {
			out = append(out, b)
		}
	}
	return out, nil
}

// Reads only cached usage: a scan never walks the registry, and a workspace nobody has pushed to has none.
func (s quotaScanner) registryBreach(workspaceID uint, threshold float64) (alerting.QuotaBreach, bool) {
	if s.registry == nil {
		return alerting.QuotaBreach{}, false
	}
	reg := s.registry.Load()
	if reg == nil {
		return alerting.QuotaBreach{}, false
	}
	return registryQuotaBreach(workspaceID, reg.CachedUsage(workspaceID), reg.QuotaMB(), threshold)
}

func registryQuotaBreach(workspaceID uint, u *registryserver.Usage, limitMB int, threshold float64) (alerting.QuotaBreach, bool) {
	if limitMB <= 0 || u == nil {
		return alerting.QuotaBreach{}, false
	}
	usedMB := u.TotalBytes / (1024 * 1024)
	if float64(usedMB)/float64(limitMB) < threshold {
		return alerting.QuotaBreach{}, false
	}
	return alerting.QuotaBreach{
		WorkspaceID: workspaceID, Resource: "registry storage", Used: int(usedMB), Limit: limitMB, Unit: "MB",
		Hint: "Delete tags you no longer need; past the quota, pushes are refused.", Link: "/registry",
	}, true
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
