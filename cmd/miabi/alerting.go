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

// backupAlerter bridges the backup service's outcome hook to the alert engine, so
// backup.Service stays decoupled from the alerting package.
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

// quotaScanner implements alerting.QuotaLister over the plan quota service and the
// per-workspace resource counts — the source for the "approaching quota" scan.
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

// platformAlerter bridges the node/runner managers' online/offline hooks to
// alerts. Platform-scoped conditions (a node, or a shared runner) are attributed
// to the reserved "system" workspace (created on demand, owned by the first
// super-admin) and fan out to super-admins; a workspace-owned runner notifies its
// own members.
type platformAlerter struct {
	e     *alerting.Engine
	ws    *repositories.WorkspaceRepository
	users *repositories.UserRepository

	mu      sync.Mutex
	sysWsID uint
}

// systemWorkspace find-or-creates the reserved system workspace and caches its id.
// Returns 0 (and skips) until a super-admin exists to own it — by which time any
// node has been registered by an admin anyway.
func (n *platformAlerter) systemWorkspace() uint {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.sysWsID != 0 {
		return n.sysWsID
	}
	if w, err := n.ws.FindByName("system"); err == nil && w != nil {
		n.sysWsID = w.ID
		return n.sysWsID
	}
	ids, err := n.users.ListAdminIDs()
	if err != nil || len(ids) == 0 {
		return 0
	}
	w := &models.Workspace{Name: "system", DisplayName: "System", OwnerID: ids[0]}
	if err := n.ws.CreateWithOwner(w); err != nil {
		return 0
	}
	n.sysWsID = w.ID
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

// RunnerStatus emits runner_offline / runner_online signals. A shared runner is
// platform-scoped (system workspace → super-admins); a workspace-owned runner
// notifies its own members.
func (n *platformAlerter) RunnerStatus(r *models.Runner, online bool) {
	var wsID uint
	platform := r.Scope == models.ScopeShared || r.WorkspaceID == nil
	if platform {
		wsID = n.systemWorkspace()
	} else {
		wsID = *r.WorkspaceID
	}
	if wsID == 0 {
		return
	}
	ref := fmt.Sprintf("runner:%d", r.ID)
	if online {
		n.e.Emit(alerting.Signal{WorkspaceID: wsID, Kind: "runner_online", Resolve: true, SubjectRef: ref, Platform: platform})
		return
	}
	name := r.DisplayName
	if name == "" {
		name = r.Name
	}
	link := fmt.Sprintf("/runners/%d", r.ID)
	if platform {
		link = "/admin/runners"
	}
	n.e.Emit(alerting.Signal{
		WorkspaceID: wsID, Kind: "runner_offline", SubjectType: "runner", SubjectRef: ref,
		SubjectLink: link, Severity: models.AlertWarning, Platform: platform,
		Title: "Runner offline — " + name,
		Body:  "The runner's tunnel dropped; queued CI/build jobs may wait for it.",
	})
}
