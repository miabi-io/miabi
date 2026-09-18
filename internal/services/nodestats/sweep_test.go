// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

	// Unreadable output must report zero, which the caller reads as "keep the last good figure"
	// rather than writing a zero that would look like a node with no disk.
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
