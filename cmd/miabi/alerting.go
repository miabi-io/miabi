// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"sync"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/alerting"
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
			// max <= 0 is unlimited; only a real, finite limit can be "near".
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
