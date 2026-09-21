// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

type recordedAlert struct {
	set        bool
	instanceID uint
	databaseID uint
	name       string
	ref        string
	errMsg     string
	ok         bool
}

type fakeAlerter struct{ calls []recordedAlert }

func (f *fakeAlerter) BackupFailed(_, dbID uint, name, errMsg string) {
	f.calls = append(f.calls, recordedAlert{databaseID: dbID, name: name, errMsg: errMsg})
}
func (f *fakeAlerter) BackupSucceeded(_, dbID uint) {
	f.calls = append(f.calls, recordedAlert{databaseID: dbID, ok: true})
}
func (f *fakeAlerter) BackupSetFailed(_, instID uint, name, ref, errMsg string) {
	f.calls = append(f.calls, recordedAlert{set: true, instanceID: instID, name: name, ref: ref, errMsg: errMsg})
}
func (f *fakeAlerter) BackupSetSucceeded(_, instID uint) {
	f.calls = append(f.calls, recordedAlert{set: true, instanceID: instID, ok: true})
}

type fakeReporter struct {
	reports []BackupReport
	ws      []uint
}

func (f *fakeReporter) ScheduledBackupFinished(ws uint, rep BackupReport) {
	f.ws = append(f.ws, ws)
	f.reports = append(f.reports, rep)
}

// Only a scheduled run reports. A manual one is watched by the person who clicked it, and telling
// them again in the inbox is how an inbox stops being read.
func TestOnlyScheduledRunsReport(t *testing.T) {
	svc, _, _ := newSetService(t)
	rep := &fakeReporter{}
	svc.SetReporter(rep)

	svc.report(1, "manual", BackupReport{Subject: "orders", OK: true})
	svc.report(1, "upgrade", BackupReport{Subject: "orders", OK: true})
	if len(rep.reports) != 0 {
		t.Fatalf("got %d reports for non-scheduled runs, want 0", len(rep.reports))
	}

	svc.report(1, "scheduled", BackupReport{Subject: "orders", OK: true})
	if len(rep.reports) != 1 {
		t.Fatalf("got %d reports for a scheduled run, want 1", len(rep.reports))
	}
	if rep.ws[0] != 1 {
		t.Errorf("reported against workspace %d, want 1", rep.ws[0])
	}
}

// An unwired reporter must be silent, not fatal: a build that never wired one still takes backups.
func TestReportWithoutAReporterIsSilent(t *testing.T) {
	svc, _, _ := newSetService(t)
	svc.report(1, "scheduled", BackupReport{Subject: "orders", OK: true})
}

func finishOne(t *testing.T, svc *Service, status models.BackupStatus, itemErr string) {
	t.Helper()
	inst := &models.DatabaseInstance{ID: 7, Name: "pg", WorkspaceID: 1}
	set := &models.DatabaseBackupSet{WorkspaceID: 1, InstanceID: 7, Ref: "mbdb_pg_x", Trigger: "scheduled"}
	if err := svc.sets.Create(set); err != nil {
		t.Fatal(err)
	}
	svc.finishSet(set, inst, []*models.Backup{
		{DatabaseID: 1, Status: status, Error: itemErr, SizeBytes: 2048},
	})
}

// A scheduled recovery point that failed used to raise nothing: the timeline recorded it and no
// alert opened, so the first anyone knew was when they went looking.
func TestFailedSetAlertsAndReports(t *testing.T) {
	svc, _, _ := newSetService(t)
	al, rep := &fakeAlerter{}, &fakeReporter{}
	svc.SetAlerter(al)
	svc.SetReporter(rep)

	finishOne(t, svc, models.BackupFailed, "connection refused")

	if len(al.calls) != 1 {
		t.Fatalf("got %d alert calls, want 1", len(al.calls))
	}
	c := al.calls[0]
	if !c.set {
		t.Error("a failed recovery point alerted through the per-database channel, where it can share a dedup key with an unrelated database")
	}
	if c.instanceID != 7 || c.name != "pg" || c.ref != "mbdb_pg_x" {
		t.Errorf("alert = instance %d/%q ref %q, want 7/pg/mbdb_pg_x", c.instanceID, c.name, c.ref)
	}
	if c.ok {
		t.Error("a failed set reported success")
	}

	if len(rep.reports) != 1 {
		t.Fatalf("got %d reports, want 1", len(rep.reports))
	}
	r := rep.reports[0]
	if r.OK || r.Ref != "mbdb_pg_x" || r.Subject != "pg" || r.Databases != 1 {
		t.Errorf("report = %+v, want a failed one naming the set, the instance and 1 database", r)
	}
	if r.Err == "" {
		t.Error("the report carries no reason, which is the only actionable part")
	}
}

func TestCompletedSetResolvesAndReports(t *testing.T) {
	svc, _, _ := newSetService(t)
	al, rep := &fakeAlerter{}, &fakeReporter{}
	svc.SetAlerter(al)
	svc.SetReporter(rep)

	finishOne(t, svc, models.BackupCompleted, "")

	if len(al.calls) != 1 || !al.calls[0].set || !al.calls[0].ok {
		t.Fatalf("calls = %+v, want one set-level success (which is what clears a firing alert)", al.calls)
	}
	if len(rep.reports) != 1 || !rep.reports[0].OK {
		t.Fatalf("reports = %+v, want one completion", rep.reports)
	}
	if rep.reports[0].SizeBytes != 2048 {
		t.Errorf("size = %d, want the summed item size 2048", rep.reports[0].SizeBytes)
	}
}

// A manual set still alerts — a failure is a failure — but posts nothing to the inbox.
func TestManualSetAlertsButDoesNotReport(t *testing.T) {
	svc, _, _ := newSetService(t)
	al, rep := &fakeAlerter{}, &fakeReporter{}
	svc.SetAlerter(al)
	svc.SetReporter(rep)

	inst := &models.DatabaseInstance{ID: 7, Name: "pg", WorkspaceID: 1}
	set := &models.DatabaseBackupSet{WorkspaceID: 1, InstanceID: 7, Ref: "mbdb_pg_m", Trigger: "manual"}
	if err := svc.sets.Create(set); err != nil {
		t.Fatal(err)
	}
	svc.finishSet(set, inst, []*models.Backup{{DatabaseID: 1, Status: models.BackupFailed, Error: "boom"}})

	if len(al.calls) != 1 {
		t.Errorf("got %d alert calls for a manual set, want 1", len(al.calls))
	}
	if len(rep.reports) != 0 {
		t.Errorf("got %d inbox reports for a manual set, want 0", len(rep.reports))
	}
}
