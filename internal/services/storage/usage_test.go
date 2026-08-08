// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// volumeRow is a sqlite-friendly stand-in for models.Volume: the production model embeds UIDModel with a
// Postgres gen_random_uuid() default sqlite can't migrate, so this builds just the columns the usage sweep
// reads and writes and lets the real VolumeRepository query them.
type volumeRow struct {
	ID             uint `gorm:"primaryKey"`
	WorkspaceID    uint
	ServerID       uint
	DockerName     string
	SizeBytes      int64
	UsedBytes      int64
	UsedMeasuredAt *time.Time
	CreatedAt      time.Time // ListByWorkspace orders by this
	UpdatedAt      time.Time // .Updates() auto-stamps this
}

func (volumeRow) TableName() string { return "volumes" }

type fakeClient struct {
	docker.Client
	usage []docker.VolumeUsage
	err   error
}

func (f fakeClient) VolumeUsage(context.Context) ([]docker.VolumeUsage, error) {
	return f.usage, f.err
}

// fakeNodes maps server id -> client, mimicking nodes.Clients.For.
type fakeNodes map[uint]docker.Client

func (n fakeNodes) For(id uint) (docker.Client, error) {
	if c, ok := n[id]; ok {
		return c, nil
	}
	return nil, errors.New("no client")
}
func (fakeNodes) LocalID() uint { return 0 }

func newUsageService(t *testing.T, nodes fakeNodes) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:usage_"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&volumeRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s := NewService(repositories.NewVolumeRepository(db), repositories.NewApplicationRepository(db), nodes)
	return s, db
}

func TestMeasureUsageRecordsMeasuredBytes(t *testing.T) {
	nodes := fakeNodes{
		1: fakeClient{usage: []docker.VolumeUsage{{DockerName: "vol-a", Bytes: 1000}, {DockerName: "vol-b", Bytes: 2000}}},
	}
	s, db := newUsageService(t, nodes)
	db.Create(&volumeRow{ID: 1, WorkspaceID: 7, ServerID: 1, DockerName: "vol-a", SizeBytes: 5000})
	db.Create(&volumeRow{ID: 2, WorkspaceID: 7, ServerID: 1, DockerName: "vol-b", SizeBytes: 5000})

	if err := s.MeasureUsage(context.Background()); err != nil {
		t.Fatalf("MeasureUsage: %v", err)
	}

	var a, b volumeRow
	db.First(&a, 1)
	db.First(&b, 2)
	if a.UsedBytes != 1000 || a.UsedMeasuredAt == nil {
		t.Fatalf("vol-a not measured: %+v", a)
	}
	if b.UsedBytes != 2000 || b.UsedMeasuredAt == nil {
		t.Fatalf("vol-b not measured: %+v", b)
	}

	// The workspace summary sums the measured usage without any live call.
	sum, err := s.WorkspaceStorage(7)
	if err != nil {
		t.Fatalf("WorkspaceStorage: %v", err)
	}
	if sum.UsedBytes != 3000 {
		t.Fatalf("used=%d, want 3000", sum.UsedBytes)
	}
	if sum.DeclaredBytes != 10000 {
		t.Fatalf("declared=%d, want 10000", sum.DeclaredBytes)
	}
	if sum.MeasuredAt == nil {
		t.Fatal("summary MeasuredAt nil after a measurement")
	}
}

func TestMeasureUsageLeavesPriorValueWhenNodeUnreachable(t *testing.T) {
	// Node 1 errors on VolumeUsage; its volume must keep its prior measurement,
	// and the sweep must not return an error to the cron.
	prior := time.Now().Add(-2 * time.Hour)
	nodes := fakeNodes{1: fakeClient{err: errors.New("node offline")}}
	s, db := newUsageService(t, nodes)
	db.Create(&volumeRow{ID: 1, WorkspaceID: 7, ServerID: 1, DockerName: "vol-a", UsedBytes: 999, UsedMeasuredAt: &prior})

	if err := s.MeasureUsage(context.Background()); err != nil {
		t.Fatalf("MeasureUsage should not error on an unreachable node: %v", err)
	}
	var a volumeRow
	db.First(&a, 1)
	if a.UsedBytes != 999 {
		t.Fatalf("prior measurement clobbered: %+v", a)
	}
	if a.UsedMeasuredAt == nil || !a.UsedMeasuredAt.Equal(prior) {
		t.Fatalf("timestamp changed on an unreachable node: %+v", a)
	}
}

func TestWorkspaceStorageNeverMeasured(t *testing.T) {
	s, db := newUsageService(t, fakeNodes{})
	db.Create(&volumeRow{ID: 1, WorkspaceID: 3, ServerID: 1, DockerName: "vol-x", SizeBytes: 4096})
	sum, err := s.WorkspaceStorage(3)
	if err != nil {
		t.Fatalf("WorkspaceStorage: %v", err)
	}
	if sum.DeclaredBytes != 4096 || sum.UsedBytes != 0 {
		t.Fatalf("declared=%d used=%d, want 4096/0", sum.DeclaredBytes, sum.UsedBytes)
	}
	if sum.MeasuredAt != nil {
		t.Fatalf("MeasuredAt should be nil before any sweep, got %v", sum.MeasuredAt)
	}
	if sum.LimitMB != -1 {
		t.Fatalf("LimitMB=%d, want -1 (no quota wired = unlimited)", sum.LimitMB)
	}
}
