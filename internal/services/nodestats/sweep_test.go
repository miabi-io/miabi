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

package nodestats

import (
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

func TestParseDF(t *testing.T) {
	// busybox df -Pk, as the helper returns it.
	out := `Filesystem           1024-blocks      Used Available Capacity Mounted on
/dev/vda1               61255492  20401152  37711140      35% /mnt/dataroot`
	total, free := parseDF(out)
	if total != 61255492*1024 {
		t.Fatalf("total = %d, want %d", total, int64(61255492)*1024)
	}
	if free != 37711140*1024 {
		t.Fatalf("free = %d, want %d", free, int64(37711140)*1024)
	}

	// Zero means "keep the last good figure", not a node with no disk.
	if tot, _ := parseDF("df: /mnt/dataroot: No such file or directory"); tot != 0 {
		t.Fatalf("total = %d on an error line, want 0", tot)
	}
	if tot, _ := parseDF(""); tot != 0 {
		t.Fatalf("total = %d on empty output, want 0", tot)
	}
}

func TestCapacityChanged(t *testing.T) {
	now := time.Now()
	const gb = int64(1) << 30
	measured := models.Server{
		CapacityMeasuredAt: &now,
		CPUCores:           8,
		MemoryBytes:        16 * gb,
		StorageBytes:       500 * gb,
		StorageFreeBytes:   250 * gb,
	}
	info := docker.Info{CPUs: 8, MemTotal: 16 * gb}

	if capacityChanged(measured, info, 500*gb, 250*gb) {
		t.Fatal("identical figures must not be written back")
	}
	// A filesystem moves constantly; that is not a capacity change worth a write.
	if capacityChanged(measured, info, 500*gb, 250*gb+(gb/1000)) {
		t.Fatal("a tiny disk fluctuation must not count as a change")
	}
	// A resized machine is always news, however small the change.
	if !capacityChanged(measured, docker.Info{CPUs: 9, MemTotal: 16 * gb}, 500*gb, 250*gb) {
		t.Fatal("a changed core count must always be recorded")
	}
	if !capacityChanged(measured, docker.Info{CPUs: 8, MemTotal: 32 * gb}, 500*gb, 250*gb) {
		t.Fatal("doubled memory must be recorded")
	}
	// Never measured: the first reading always writes.
	if !capacityChanged(models.Server{}, info, 500*gb, 250*gb) {
		t.Fatal("the first measurement must always be recorded")
	}
}
