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

package storageclass

import (
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

func TestDataRootDisks(t *testing.T) {
	older := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	servers := []models.Server{
		{ID: 1, Name: "local", IsLocal: true, StorageBytes: 100, StorageFreeBytes: 40, CapacityMeasuredAt: &newer},
		{ID: 2, Name: "edge", DisplayName: "Edge 1", StorageBytes: 50, StorageFreeBytes: 10, CapacityMeasuredAt: &older},
		{ID: 3, Name: "never-measured"},
	}
	disks, capacity, available, at := dataRootDisks(servers)
	if len(disks) != 3 || disks[1].ServerName != "Edge 1" || !disks[0].IsLocal {
		t.Fatalf("disks: %+v", disks)
	}
	if capacity != 150 || available != 50 {
		t.Fatalf("totals: %d / %d, want 150 / 50 (the unmeasured node is left out)", capacity, available)
	}
	if at == nil || !at.Equal(older) {
		t.Fatalf("measured at %v, want the oldest reading %v", at, older)
	}
	if disks[2].CapacityBytes != 0 || disks[2].MeasuredAt != nil {
		t.Fatalf("unmeasured node: %+v", disks[2])
	}

	if _, c, _, at := dataRootDisks([]models.Server{{ID: 9}}); c != 0 || at != nil {
		t.Fatalf("no measurement: capacity %d, at %v", c, at)
	}
}
