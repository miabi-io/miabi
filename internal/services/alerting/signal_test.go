// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// TestSignalBackupFireResolve exercises the generic Signal pipeline: a
// backup_failed fires one workspace-scoped alert; a backup_ok resolves it.
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

// TestNodeAlertFansToSystemAdmins: a platform (node) alert reaches the super-admins,
// never the workspace members, and auto-resolves on reconnect.
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

// TestRunnerAlertScopes: a workspace runner notifies its members (not admins); a
// shared runner (Platform) notifies super-admins (not workspace members).
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

// TestDiskScanFireAndResolve: a volume ≥95% full fires a critical disk alert;
// once usage drops below the warning threshold it auto-resolves.
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

// TestQuotaScanFireAndResolve: a near-quota breach fires a warning; clearing the
// breach resolves it.
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

// TestCertScanFireAndResolve proves the self-contained TLS scanner: a cert
// expiring inside the critical window fires one alert; once it drops out of the
// scan (renewed), the next scan auto-resolves it.
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
