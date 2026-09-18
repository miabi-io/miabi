// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
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

type serverTable struct {
	ID                 uint `gorm:"primaryKey"`
	Name               string
	Status             string
	IsLocal            bool
	ClusterID          uint
	CPUCores           int
	MemoryBytes        int64
	StorageBytes       int64
	StorageFreeBytes   int64
	CapacityMeasuredAt *time.Time
	CPUPercent         float64
	MemUsedBytes       int64
	UsageMeasuredAt    *time.Time
}

func (serverTable) TableName() string { return "servers" }

// The committed sums read these; present so the sums return a real zero rather than an error.
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

// Capacity is read from what the node sweep stored, so the dashboard is a query: it must not grow
// slower as nodes are added, and a node nobody has measured must not read as a node with none.
func TestFleetSumsOnlyMeasuredNodes(t *testing.T) {
	db := metricsDB(t)
	now := time.Now()
	stale := now.Add(-time.Hour)
	rows := []serverTable{
		// Measured, with fresh usage.
		{Name: "a", CPUCores: 8, MemoryBytes: 16 << 30, StorageBytes: 500 << 30, StorageFreeBytes: 200 << 30,
			CapacityMeasuredAt: &now, CPUPercent: 50, MemUsedBytes: 8 << 30, UsageMeasuredAt: &now},
		// Measured, but its usage stopped being reported an hour ago.
		{Name: "b", CPUCores: 4, MemoryBytes: 8 << 30, StorageBytes: 100 << 30, StorageFreeBytes: 50 << 30,
			CapacityMeasuredAt: &now, CPUPercent: 90, MemUsedBytes: 7 << 30, UsageMeasuredAt: &stale},
		// Never measured at all: counted as a node, contributes nothing.
		{Name: "c"},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed %s: %v", rows[i].Name, err)
		}
	}

	h := &AdminMetricsHandler{db: db, nodeCapacity: repositories.NewServerRepository(db)}
	got := h.fleet()
	if got == nil {
		t.Fatal("fleet returned nil with a capacity store wired")
	}
	if got.NodesTotal != 3 || got.NodesCounted != 2 {
		t.Fatalf("nodes total=%d measured=%d, want 3/2", got.NodesTotal, got.NodesCounted)
	}
	if got.CPUCores != 12 || got.MemoryBytes != 24<<30 {
		t.Fatalf("capacity = %d cores / %d bytes, want 12 / %d", got.CPUCores, got.MemoryBytes, int64(24)<<30)
	}
	if got.StorageBytes != 600<<30 || got.StorageFreeBytes != 250<<30 {
		t.Fatalf("storage = %d / %d free", got.StorageBytes, got.StorageFreeBytes)
	}
	// Only node a has usage recent enough to count.
	if got.NodesSampled != 1 || got.MemoryUsedBytes != 8<<30 {
		t.Fatalf("usage nodes=%d mem=%d, want 1 / %d", got.NodesSampled, got.MemoryUsedBytes, int64(8)<<30)
	}
	if got.CPUPercent != 50 {
		t.Fatalf("cpu percent = %v, want 50 (the stale node must not drag it up)", got.CPUPercent)
	}
}

// CPU is averaged by core count, so a busy big node outweighs an idle small one.
func TestFleetCPUIsWeightedByCores(t *testing.T) {
	db := metricsDB(t)
	now := time.Now()
	rows := []serverTable{
		{Name: "big", CPUCores: 30, MemoryBytes: 1, CapacityMeasuredAt: &now, CPUPercent: 100, UsageMeasuredAt: &now},
		{Name: "small", CPUCores: 10, MemoryBytes: 1, CapacityMeasuredAt: &now, CPUPercent: 0, UsageMeasuredAt: &now},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	h := &AdminMetricsHandler{db: db, nodeCapacity: repositories.NewServerRepository(db)}
	if got := h.fleet().CPUPercent; got != 75 {
		t.Fatalf("cpu percent = %v, want 75 (30 busy cores of 40)", got)
	}
}
