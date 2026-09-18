// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/docker"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Shadow tables mirror the models without the Postgres-only uid default sqlite cannot parse, the
// same way the repository tests do. Rows are seeded through these; the handler reads the real models.
type storageClassTable struct {
	ID             uint `gorm:"primaryKey"`
	Name           string
	DisplayName    string
	Path           string
	Enabled        bool
	Builtin        bool
	CapacityBytes  int64
	AvailableBytes int64
}

func (storageClassTable) TableName() string { return "storage_classes" }

type certificateTable struct {
	ID       uint `gorm:"primaryKey"`
	Name     string
	NotAfter time.Time
}

func (certificateTable) TableName() string { return "certificates" }

type alertTable struct {
	ID    uint `gorm:"primaryKey"`
	State string
}

func (alertTable) TableName() string { return "alerts" }

type platformBackupTable struct {
	ID         uint `gorm:"primaryKey"`
	Status     string
	CreatedAt  time.Time
	FinishedAt *time.Time
}

func (platformBackupTable) TableName() string { return "platform_backups" }

func metricsDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&storageClassTable{}, &certificateTable{}, &alertTable{}, &platformBackupTable{},
		&applicationTable{}, &databaseInstanceTable{}, &serverTable{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// The fullest class is what breaks first, so that is the one the dashboard names — not an average,
// which two half-empty disks would hide a full one behind.
func TestStorageClassStatsReportsTheFullestClass(t *testing.T) {
	db := metricsDB(t)
	h := &AdminMetricsHandler{db: db}
	for _, c := range []storageClassTable{
		{Name: "default", DisplayName: "Default", Builtin: true, Enabled: true},
		{Name: "bulk", DisplayName: "Bulk (ssd3)", Path: "/mnt/ssd3/miabi", Enabled: true, CapacityBytes: 1000, AvailableBytes: 900},
		{Name: "ssd-fast", DisplayName: "NVMe (ssd1)", Path: "/mnt/ssd1/miabi", Enabled: true, CapacityBytes: 1000, AvailableBytes: 50},
		{Name: "cold", Path: "/mnt/ssd2/miabi", Enabled: true}, // registered, never swept
	} {
		if err := db.Create(&c).Error; err != nil {
			t.Fatalf("seed %s: %v", c.Name, err)
		}
	}

	got := h.storageClasses()
	if got.Total != 4 || got.Managed != 3 {
		t.Fatalf("total=%d managed=%d, want 4/3", got.Total, got.Managed)
	}
	if got.FullestName != "NVMe (ssd1)" || got.FullestPct != 95 {
		t.Fatalf("fullest = %q at %d%%, want NVMe (ssd1) at 95%%", got.FullestName, got.FullestPct)
	}
	if got.Unmeasured != 1 {
		t.Fatalf("unmeasured = %d, want 1 (a class its node never answered for)", got.Unmeasured)
	}
	if got.AvailableBytes != 950 {
		t.Fatalf("available = %d, want 950 (measured classes only)", got.AvailableBytes)
	}
}

func TestSignalsSeparatesExpiredFromExpiring(t *testing.T) {
	db := metricsDB(t)
	h := &AdminMetricsHandler{db: db}
	now := time.Now()
	for _, c := range []certificateTable{
		{Name: "gone", NotAfter: now.Add(-time.Hour)},
		{Name: "soon", NotAfter: now.Add(3 * 24 * time.Hour)},
		{Name: "fine", NotAfter: now.Add(90 * 24 * time.Hour)},
	} {
		if err := db.Create(&c).Error; err != nil {
			t.Fatalf("seed cert: %v", err)
		}
	}
	if err := db.Create(&alertTable{State: string(models.AlertFiring)}).Error; err != nil {
		t.Fatalf("seed alert: %v", err)
	}
	if err := db.Create(&alertTable{State: string(models.AlertResolved)}).Error; err != nil {
		t.Fatalf("seed alert: %v", err)
	}

	got := h.signals()
	if got.CertsExpired != 1 {
		t.Fatalf("expired = %d, want 1", got.CertsExpired)
	}
	if got.CertsExpiringSoon != 1 {
		t.Fatalf("expiring soon = %d, want 1 (the 90-day one is not soon)", got.CertsExpiringSoon)
	}
	if got.FiringAlerts != 1 {
		t.Fatalf("firing alerts = %d, want 1 (a resolved alert is not firing)", got.FiringAlerts)
	}
	if got.LastBackupAt != nil {
		t.Fatalf("last backup = %v, want nil when none has ever completed", got.LastBackupAt)
	}
}

// A failed newest backup must read as failed even though an older one succeeded — otherwise the
// dashboard reports the last success and nobody learns that backups started failing.
func TestSignalsReportsAFailedNewestBackup(t *testing.T) {
	db := metricsDB(t)
	h := &AdminMetricsHandler{db: db}
	old := time.Now().Add(-48 * time.Hour)
	if err := db.Create(&platformBackupTable{Status: string(models.BackupCompleted), CreatedAt: old, FinishedAt: &old}).Error; err != nil {
		t.Fatalf("seed completed: %v", err)
	}
	if err := db.Create(&platformBackupTable{Status: string(models.BackupFailed), CreatedAt: time.Now()}).Error; err != nil {
		t.Fatalf("seed failed: %v", err)
	}

	got := h.signals()
	if got.LastBackupAt == nil || !got.LastBackupAt.Equal(old) {
		t.Fatalf("last backup = %v, want the older successful one", got.LastBackupAt)
	}
	if !got.LastBackupFailed {
		t.Fatal("the newest backup failed; the signal must say so")
	}
}

// slowClients stands in for a fleet where every node takes a while to answer.
type slowClients struct {
	delay time.Duration
	calls int32
}

func (s *slowClients) For(uint) (docker.Client, error) {
	atomic.AddInt32(&s.calls, 1)
	time.Sleep(s.delay)
	return nil, errors.New("probe finished")
}

// The dashboard must not get slower as nodes are added: the probe belongs behind the cache, not on
// the request. This is the regression that made it feel broken on a multi-node install.
func TestFleetNeverProbesOnTheRequestPath(t *testing.T) {
	db := metricsDB(t)
	clients := &slowClients{delay: 300 * time.Millisecond}
	h := &AdminMetricsHandler{db: db, nodeClients: clients}
	for i := 0; i < 6; i++ {
		if err := db.Create(&serverTable{Name: "node", Status: "online"}).Error; err != nil {
			t.Fatalf("seed node: %v", err)
		}
	}

	start := time.Now()
	got := h.fleet(context.Background())
	elapsed := time.Since(start)

	if got == nil {
		t.Fatal("fleet returned nil with clients wired")
	}
	// Six nodes at 300ms each is ~1.8s sequentially, and ~300ms even probed in parallel. The
	// request itself must be neither.
	if elapsed > 150*time.Millisecond {
		t.Fatalf("fleet() took %v — it is probing on the request path", elapsed)
	}
}

// A second caller arriving while a refresh is running must not start another one.
func TestFleetRefreshIsNotStartedTwice(t *testing.T) {
	db := metricsDB(t)
	clients := &slowClients{delay: 200 * time.Millisecond}
	h := &AdminMetricsHandler{db: db, nodeClients: clients}
	if err := db.Create(&serverTable{Name: "node", Status: "online"}).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}

	for i := 0; i < 5; i++ {
		h.fleet(context.Background())
	}
	time.Sleep(400 * time.Millisecond) // let the one refresh finish

	if n := atomic.LoadInt32(&clients.calls); n != 1 {
		t.Fatalf("node probed %d times across 5 concurrent-ish reads, want 1", n)
	}
}

// Limits the committed sums read; present so the sums return a real zero rather than an error.
type applicationTable struct {
	ID          uint `gorm:"primaryKey"`
	NanoCPUs    int64
	MemoryBytes int64
}

func (applicationTable) TableName() string { return "applications" }

type databaseInstanceTable struct {
	ID          uint `gorm:"primaryKey"`
	NanoCPUs    int64
	MemoryBytes int64
}

func (databaseInstanceTable) TableName() string { return "database_instances" }

type serverTable struct {
	ID      uint `gorm:"primaryKey"`
	Name    string
	Status  string
	IsLocal bool
}

func (serverTable) TableName() string { return "servers" }
