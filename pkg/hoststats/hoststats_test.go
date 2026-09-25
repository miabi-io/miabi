// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: Apache-2.0

package hoststats

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCPUPercent(t *testing.T) {
	// Between samples: total +1000 jiffies, idle +250 → 75% busy.
	got := CPUPercent(CPUTimes{Total: 1000, Idle: 500}, CPUTimes{Total: 2000, Idle: 750})
	if got != 75 {
		t.Fatalf("CPUPercent = %v, want 75", got)
	}
	// No movement (or counter reset) → 0, never negative.
	if got := CPUPercent(CPUTimes{Total: 2000, Idle: 750}, CPUTimes{Total: 2000, Idle: 750}); got != 0 {
		t.Fatalf("idle delta = 0 case = %v, want 0", got)
	}
}

func TestReadMem(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "meminfo"),
		"MemTotal:       16384000 kB\nMemFree:         1000000 kB\nMemAvailable:    4096000 kB\nBuffers:          200000 kB\n")
	total, avail, err := ReadMem(dir)
	if err != nil {
		t.Fatal(err)
	}
	if total != 16384000*1024 {
		t.Errorf("total = %d", total)
	}
	if avail != 4096000*1024 {
		t.Errorf("avail = %d", avail)
	}
}

func TestReadAndAvailable(t *testing.T) {
	dir := t.TempDir()
	if Available(dir) {
		t.Fatal("Available should be false with no stat file")
	}
	mustWrite(t, filepath.Join(dir, "stat"), "cpu  100 0 100 800 0 0 0 0 0 0\nintr 1\n")
	mustWrite(t, filepath.Join(dir, "meminfo"), "MemTotal:        8192000 kB\nMemAvailable:    2048000 kB\n")
	if !Available(dir) {
		t.Fatal("Available should be true once stat exists")
	}
	st, err := Read(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.MemTotalBytes != 8192000*1024 {
		t.Errorf("MemTotalBytes = %d", st.MemTotalBytes)
	}
	// used = total - available = 6144000 kB
	if st.MemUsedBytes != (8192000-2048000)*1024 {
		t.Errorf("MemUsedBytes = %d", st.MemUsedBytes)
	}
	if st.MemPercent < 74 || st.MemPercent > 76 {
		t.Errorf("MemPercent = %v, want ~75", st.MemPercent)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
