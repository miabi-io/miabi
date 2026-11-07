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

package alerting

import (
	"context"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

type fakeCerts struct {
	expiring []models.Certificate
	failed   []models.Certificate
}

func (f fakeCerts) ListExpiringBefore(time.Time) ([]models.Certificate, error) {
	return f.expiring, nil
}
func (f fakeCerts) ListByStatus(string) ([]models.Certificate, error) { return f.failed, nil }

func TestSignalBackupFireResolve(t *testing.T) {
	eng, alerts, inbox, _ := newEngineDB(t)

	eng.Emit(Signal{
		WorkspaceID: 1, Kind: "backup_failed", SubjectType: "database",
		SubjectRef: "database:3", SubjectLink: "/databases/3",
		Severity: models.AlertCritical, Title: "Backup failed — main", Body: "pg_dump exited 1",
	})

	active, _ := alerts.ListByWorkspace(1, true, 0)
	if len(active) != 1 || active[0].DedupKey != "backup_failed:database:3" || active[0].Category != models.CategoryDatabase {
		t.Fatalf("want 1 active backup alert, got %+v", active)
	}
	if ns, _ := inbox.ListByUser(7, 0, false, 0, 100); len(ns) != 1 {
		t.Fatalf("developer should have 1 notification, got %d", len(ns))
	}

	eng.Emit(Signal{WorkspaceID: 1, Kind: "backup_ok", SubjectRef: "database:3", Resolve: true})
	if act, _ := alerts.ListByWorkspace(1, true, 0); len(act) != 0 {
		t.Fatalf("backup alert should be resolved, %d active", len(act))
	}
}

func TestEvaluateSignalUnknownKindIgnored(t *testing.T) {
	if got := evaluateSignal(Signal{WorkspaceID: 1, Kind: "nope", SubjectRef: "x:1"}); got != nil {
		t.Fatalf("unknown kind should yield no intents, got %v", got)
	}
	if got := evaluateSignal(Signal{WorkspaceID: 1, Kind: "backup_failed"}); got != nil {
		t.Fatalf("missing subject ref should yield no intents")
	}
}

type volLister struct{ v []models.Volume }

func (f volLister) ListAll() ([]models.Volume, error) { return f.v, nil }

type quotaLister struct{ b []QuotaBreach }

func (f quotaLister) NearQuota(float64) ([]QuotaBreach, error) { return f.b, nil }

type fakeAdmins struct{ ids []uint }

func (f fakeAdmins) ListAdminIDs() ([]uint, error) { return f.ids, nil }

func TestNodeAlertFansToSystemAdmins(t *testing.T) {
	eng, alerts, inbox, _ := newEngineDB(t)
	eng.SetSystemAdmins(fakeAdmins{ids: []uint{100, 101}})
	const sysWs = 999

	eng.Emit(Signal{
		WorkspaceID: sysWs, Kind: "node_offline", SubjectType: "node", SubjectRef: "node:2",
		SubjectLink: "/admin/nodes/2", Severity: models.AlertCritical, Title: "Node offline — edge-2",
	})

	active, _ := alerts.ListByWorkspace(sysWs, true, 0)
	if len(active) != 1 || active[0].DedupKey != "node_offline:node:2" || active[0].Category != models.CategoryNode {
		t.Fatalf("want 1 node alert, got %+v", active)
	}
	// Super-admins receive it; workspace members (7 developer, 9 viewer) do not.
	for _, admin := range []uint{100, 101} {
		if c, _ := inbox.UnreadCount(admin); c != 1 {
			t.Fatalf("admin %d should have 1 unread, got %d", admin, c)
		}
	}
	if c, _ := inbox.UnreadCount(7); c != 0 {
		t.Fatalf("workspace member must NOT receive a platform alert, got %d", c)
	}

	// Reconnect resolves it.
	eng.Emit(Signal{WorkspaceID: sysWs, Kind: "node_online", Resolve: true, SubjectRef: "node:2"})
	if act, _ := alerts.ListByWorkspace(sysWs, true, 0); len(act) != 0 {
		t.Fatalf("node alert should resolve on reconnect, %d active", len(act))
	}
}

func TestRunnerAlertScopes(t *testing.T) {
	eng, _, inbox, _ := newEngineDB(t)
	eng.SetSystemAdmins(fakeAdmins{ids: []uint{100}})

	// Workspace-owned runner (not platform) → workspace developer 7, not admin 100.
	eng.Emit(Signal{
		WorkspaceID: 1, Kind: "runner_offline", SubjectType: "runner", SubjectRef: "runner:5",
		Severity: models.AlertWarning, Title: "Runner offline — build-1",
	})
	if c, _ := inbox.UnreadCount(7); c != 1 {
		t.Fatalf("workspace member should get a workspace-runner alert, got %d", c)
	}
	if c, _ := inbox.UnreadCount(100); c != 0 {
		t.Fatalf("admin should NOT get a workspace-runner alert, got %d", c)
	}

	// Shared runner (Platform=true) → admin 100, not workspace member 7.
	eng.Emit(Signal{
		WorkspaceID: 999, Kind: "runner_offline", SubjectRef: "runner:9",
		Severity: models.AlertWarning, Title: "Runner offline — shared-1", Platform: true,
	})
	if c, _ := inbox.UnreadCount(100); c != 1 {
		t.Fatalf("admin should get a shared-runner alert, got %d", c)
	}
	if c, _ := inbox.UnreadCount(7); c != 1 {
		t.Fatalf("member must not receive the shared-runner alert (still 1), got %d", c)
	}
}

type runnerLister struct{ r []models.Runner }

func (f *runnerLister) ListAll() ([]models.Runner, error) { return f.r, nil }

func ago(d time.Duration) *time.Time { t := time.Now().UTC().Add(-d); return &t }

// A runner returning inside the window must never reach an inbox, and flapping must not resolve/re-fire per cycle.
func TestRunnerScanDebounce(t *testing.T) {
	eng, alerts, inbox, _ := newEngineDB(t)
	ws := uint(1)
	rl := &runnerLister{r: []models.Runner{{
		ID: 5, Name: "build-1", WorkspaceID: &ws, Scope: models.ScopeWorkspace,
		Enabled: true, Status: models.RunnerStatusOffline, LastSeenAt: ago(30 * time.Second),
	}}}
	eng.SetRunnerLister(rl)
	run := func() { eng.scanRunners(context.Background()) }
	active := func() int { a, _ := alerts.ListByWorkspace(1, true, 0); return len(a) }

	// Offline for 30s — a restart, not an outage. Nothing said.
	run()
	if active() != 0 {
		t.Fatalf("a 30s blip must not alert, %d active", active())
	}

	// Past the threshold: one alert, one notification.
	rl.r[0].LastSeenAt = ago(3 * time.Minute)
	run()
	if active() != 1 {
		t.Fatalf("want 1 alert after 3 minutes offline, got %d", active())
	}
	if c, _ := inbox.UnreadCount(7); c != 1 {
		t.Fatalf("developer should have 1 notification, got %d", c)
	}

	// Held, not resolved: stops a flapping runner emitting recovered/offline pairs every cycle.
	rl.r[0].Status = models.RunnerStatusOnline
	rl.r[0].LastSeenAt = ago(time.Second)
	rl.r[0].ConnectedSince = ago(30 * time.Second)
	run()
	if active() != 1 {
		t.Fatalf("alert must hold while the runner is only briefly back, got %d active", active())
	}

	// Stably back: resolved.
	rl.r[0].ConnectedSince = ago(3 * time.Minute)
	run()
	if active() != 0 {
		t.Fatalf("alert should clear once the runner is stably back, %d active", active())
	}
}

// Ephemeral runners are torn down by design and disabled ones on purpose; neither is news.
func TestRunnerScanSkipsExpectedAbsence(t *testing.T) {
	eng, alerts, _, _ := newEngineDB(t)
	ws := uint(1)
	long := ago(time.Hour)
	eng.SetRunnerLister(&runnerLister{r: []models.Runner{
		{ID: 1, Name: "ephemeral", WorkspaceID: &ws, Scope: models.ScopeWorkspace,
			Enabled: true, Ephemeral: true, Status: models.RunnerStatusOffline, LastSeenAt: long},
		{ID: 2, Name: "disabled", WorkspaceID: &ws, Scope: models.ScopeWorkspace,
			Enabled: false, Status: models.RunnerStatusOffline, LastSeenAt: long},
		{ID: 3, Name: "never-started", WorkspaceID: &ws, Scope: models.ScopeWorkspace,
			Enabled: true, Status: models.RunnerStatusOffline}, // no LastSeenAt
	}})

	eng.scanRunners(context.Background())
	if a, _ := alerts.ListByWorkspace(1, true, 0); len(a) != 0 {
		t.Fatalf("expected absences must not alert, got %+v", a)
	}
}

func TestRunnerScanSharedGoesToAdmins(t *testing.T) {
	eng, alerts, inbox, _ := newEngineDB(t)
	eng.SetSystemAdmins(fakeAdmins{ids: []uint{100}})
	eng.SetSystemWorkspace(func() uint { return 999 })
	eng.SetRunnerLister(&runnerLister{r: []models.Runner{{
		ID: 9, Name: "shared-1", Scope: models.ScopeShared, // WorkspaceID nil
		Enabled: true, Status: models.RunnerStatusOffline, LastSeenAt: ago(5 * time.Minute),
	}}})

	eng.scanRunners(context.Background())
	act, _ := alerts.ListByWorkspace(999, true, 0)
	if len(act) != 1 || act[0].DedupKey != "runner_offline:runner:9" {
		t.Fatalf("want 1 platform runner alert, got %+v", act)
	}
	if c, _ := inbox.UnreadCount(100); c != 1 {
		t.Fatalf("admin should be notified, got %d", c)
	}
	if c, _ := inbox.UnreadCount(7); c != 0 {
		t.Fatalf("workspace member must not receive a shared-runner alert, got %d", c)
	}
}

// Must be skipped rather than written to workspace 0.
func TestRunnerScanSkipsSharedWithoutSystemWorkspace(t *testing.T) {
	eng, alerts, _, _ := newEngineDB(t)
	eng.SetRunnerLister(&runnerLister{r: []models.Runner{{
		ID: 9, Name: "shared-1", Scope: models.ScopeShared,
		Enabled: true, Status: models.RunnerStatusOffline, LastSeenAt: ago(5 * time.Minute),
	}}})

	eng.scanRunners(context.Background())
	if a, _ := alerts.ListByWorkspace(0, true, 0); len(a) != 0 {
		t.Fatalf("must not alert without a system workspace, got %+v", a)
	}
}

func TestDiskScanFireAndResolve(t *testing.T) {
	eng, alerts, _, _ := newEngineDB(t)
	now := time.Now().UTC()
	vols := &volLister{v: []models.Volume{
		{ID: 3, WorkspaceID: 1, Name: "data", SizeBytes: 100, UsedBytes: 96, UsedMeasuredAt: &now},
		{ID: 4, WorkspaceID: 1, Name: "small", SizeBytes: 100, UsedBytes: 10, UsedMeasuredAt: &now}, // healthy
	}}
	eng.SetVolumeLister(vols)

	eng.scanVolumes(context.Background())
	active, _ := alerts.ListByWorkspace(1, true, 0)
	if len(active) != 1 || active[0].DedupKey != "disk_near:volume:3" || active[0].Severity != models.AlertCritical {
		t.Fatalf("want 1 critical disk alert for volume 3, got %+v", active)
	}

	vols.v[0].UsedBytes = 20 // pruned
	eng.scanVolumes(context.Background())
	if act, _ := alerts.ListByWorkspace(1, true, 0); len(act) != 0 {
		t.Fatalf("disk alert should auto-resolve, %d active", len(act))
	}
}

func TestQuotaScanFireAndResolve(t *testing.T) {
	eng, alerts, _, _ := newEngineDB(t)
	q := &quotaLister{b: []QuotaBreach{{WorkspaceID: 1, Resource: "apps", Used: 9, Limit: 10}}}
	eng.SetQuotaLister(q)

	eng.scanQuotas(context.Background())
	active, _ := alerts.ListByWorkspace(1, true, 0)
	if len(active) != 1 || active[0].DedupKey != "quota_near:quota:apps" || active[0].Category != models.CategoryQuota {
		t.Fatalf("want 1 quota alert, got %+v", active)
	}

	q.b = nil // no longer near
	eng.scanQuotas(context.Background())
	if act, _ := alerts.ListByWorkspace(1, true, 0); len(act) != 0 {
		t.Fatalf("quota alert should resolve, %d active", len(act))
	}
}

func TestCertScanFireAndResolve(t *testing.T) {
	eng, alerts, inbox, _ := newEngineDB(t)
	soon := time.Now().UTC().Add(2 * 24 * time.Hour) // < 3d → critical
	certs := &fakeCerts{expiring: []models.Certificate{
		{ID: 8, WorkspaceID: 1, Name: "web", DisplayName: "web.example.com", NotAfter: soon},
	}}
	eng.SetCertLister(certs)

	eng.scanCerts(context.Background())
	active, _ := alerts.ListByWorkspace(1, true, 0)
	if len(active) != 1 || active[0].DedupKey != "cert_expiring:cert:8" || active[0].Severity != models.AlertCritical {
		t.Fatalf("want 1 critical cert alert, got %+v", active)
	}
	if active[0].SubjectLink != "/certificates/8" {
		t.Fatalf("cert deep-link wrong: %q", active[0].SubjectLink)
	}
	if c, _ := inbox.UnreadCount(7); c != 1 {
		t.Fatalf("developer unread = %d, want 1", c)
	}

	// A repeat scan while still expiring must NOT create a second alert (dedup).
	eng.scanCerts(context.Background())
	if act, _ := alerts.ListByWorkspace(1, true, 0); len(act) != 1 {
		t.Fatalf("repeat scan created duplicate alerts: %d", len(act))
	}

	// Renewed: the cert no longer appears in the scan → auto-resolve.
	certs.expiring = nil
	eng.scanCerts(context.Background())
	if act, _ := alerts.ListByWorkspace(1, true, 0); len(act) != 0 {
		t.Fatalf("cert alert should auto-resolve after renewal, %d active", len(act))
	}
}
