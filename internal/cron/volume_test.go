// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cron

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

type fakeVolumeScheduler struct {
	schedules []models.VolumeBackupSchedule
	ran       [][2]uint
}

func (f *fakeVolumeScheduler) ListEnabledSchedules() ([]models.VolumeBackupSchedule, error) {
	return f.schedules, nil
}

func (f *fakeVolumeScheduler) RunSchedule(_ context.Context, ws, id uint) error {
	f.ran = append(f.ran, [2]uint{ws, id})
	return nil
}

func TestSetVolumeBackupsRegistersEnabledSchedules(t *testing.T) {
	m := NewManager(nil, nil, nil, nil, nil)
	m.SetVolumeBackups(&fakeVolumeScheduler{schedules: []models.VolumeBackupSchedule{
		{ID: 3, WorkspaceID: 1, VolumeID: 9, Cron: "0 3 * * *", Enabled: true},
		{ID: 4, WorkspaceID: 1, VolumeID: 9, Cron: "not a cron", Enabled: true},
	}})
	var kinds []string
	for _, j := range m.Snapshot() {
		kinds = append(kinds, j.Kind)
		if j.ID != 3 {
			t.Errorf("registered schedule %d; an invalid cron must be skipped, not registered", j.ID)
		}
	}
	if len(kinds) != 1 || kinds[0] != "volume-backup" {
		t.Fatalf("kinds = %v, want one volume-backup task", kinds)
	}
}

// The licence gate stops a scheduled point before any work is enqueued; an expired licence
// degrades configuration, and taking new points is configuration's output.
func TestVolumeBackupRunIsGated(t *testing.T) {
	m := NewManager(nil, nil, nil, nil, nil)
	f := &fakeVolumeScheduler{}
	m.SetVolumeBackups(f)
	denied := errors.New("license required")
	m.SetRecoveryPointGate(func() error { return denied })
	if err := m.runVolumeBackup(3, 1); !errors.Is(err, denied) {
		t.Fatalf("err = %v, want the gate's error", err)
	}
	if len(f.ran) != 0 {
		t.Fatal("a gated schedule still ran")
	}
	m.SetRecoveryPointGate(func() error { return nil })
	if err := m.runVolumeBackup(3, 1); err != nil {
		t.Fatal(err)
	}
	if len(f.ran) != 1 || f.ran[0] != [2]uint{1, 3} {
		t.Fatalf("ran = %v, want workspace 1 schedule 3", f.ran)
	}
}
