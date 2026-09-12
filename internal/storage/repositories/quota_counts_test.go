// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type appComputeRow struct {
	ID          uint `gorm:"primaryKey"`
	WorkspaceID uint
	NanoCPUs    int64
	MemoryBytes int64
	Replicas    int
	RuntimeKind string
	DeletedAt   gorm.DeletedAt `gorm:"index"`
}

func (appComputeRow) TableName() string { return "applications" }

func TestSumResourcesCountsServiceReplicas(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&appComputeRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	rows := []appComputeRow{
		{ID: 1, WorkspaceID: 1, NanoCPUs: 5e8, MemoryBytes: 512, Replicas: 4, RuntimeKind: "service"},
		{ID: 2, WorkspaceID: 1, NanoCPUs: 1e9, MemoryBytes: 256, Replicas: 3, RuntimeKind: "container"},
		{ID: 3, WorkspaceID: 1, NanoCPUs: 1e9, MemoryBytes: 100, Replicas: 0, RuntimeKind: "service"},
		{ID: 4, WorkspaceID: 2, NanoCPUs: 9e9, MemoryBytes: 999, Replicas: 1, RuntimeKind: "service"},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	repo := NewApplicationRepository(db)

	cpu, mem, err := repo.SumResourcesByWorkspace(1, 0)
	if err != nil {
		t.Fatalf("sum: %v", err)
	}
	if cpu != 4e9 || mem != 4*512+256+100 {
		t.Errorf("sum = %d nano / %d bytes, want 4e9 / %d", cpu, mem, 4*512+256+100)
	}

	cpu, mem, err = repo.SumResourcesByWorkspace(1, 1)
	if err != nil {
		t.Fatalf("sum excluding: %v", err)
	}
	if cpu != 2e9 || mem != 356 {
		t.Errorf("sum excluding the service = %d nano / %d bytes, want 2e9 / 356", cpu, mem)
	}
}
