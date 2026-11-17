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

package repositories

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newGPUDB(t *testing.T) *GPUDeviceRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.GPUDevice{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewGPUDeviceRepository(db)
}

func TestGPUMarkMissing(t *testing.T) {
	r := newGPUDB(t)
	for _, u := range []string{"GPU-a", "GPU-b"} {
		if err := r.Upsert(1, models.GPUDevice{UUID: u, Vendor: models.GPUVendorNvidia}); err != nil {
			t.Fatalf("upsert %s: %v", u, err)
		}
	}
	if err := r.Upsert(2, models.GPUDevice{UUID: "GPU-other-node", Vendor: models.GPUVendorNvidia}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	for _, u := range []string{"GPU-a", "GPU-b", "GPU-other-node"} {
		d, _ := r.FindByUUID(u)
		d.Enabled = true
		if err := r.Update(d); err != nil {
			t.Fatalf("enable %s: %v", u, err)
		}
	}

	n, err := r.MarkMissing(1, []string{"GPU-a"})
	if err != nil || n != 1 {
		t.Fatalf("MarkMissing = %d, %v; want 1, nil", n, err)
	}
	enabled, _ := r.ListEnabledByServer(1)
	if len(enabled) != 1 || enabled[0].UUID != "GPU-a" {
		t.Errorf("a missing card must not be offered, got %+v", enabled)
	}
	if other, _ := r.FindByUUID("GPU-other-node"); other.Missing {
		t.Error("a sweep of node 1 must not touch node 2's cards")
	}

	if err := r.Upsert(1, models.GPUDevice{UUID: "GPU-b", Vendor: models.GPUVendorNvidia}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	b, _ := r.FindByUUID("GPU-b")
	if b.Missing || !b.Enabled {
		t.Errorf("a returning card should clear missing and keep its admin flags, got missing=%v enabled=%v", b.Missing, b.Enabled)
	}

	if n, _ := r.MarkMissing(1, nil); n != 2 {
		t.Errorf("an empty successful probe should mark every node-1 card missing, got %d", n)
	}
}
