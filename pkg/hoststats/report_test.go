// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: Apache-2.0

package hoststats

import (
	"errors"
	"path/filepath"
	"testing"
)

func writeProc(t *testing.T, dir, statLine string) {
	t.Helper()
	mustWrite(t, filepath.Join(dir, "stat"), statLine+"\nintr 1\n")
	mustWrite(t, filepath.Join(dir, "meminfo"), "MemTotal:        8192000 kB\nMemAvailable:    2048000 kB\n")
	mustWrite(t, filepath.Join(dir, "loadavg"), "0.42 0.30 0.20 1/234 5678\n")
	mustWrite(t, filepath.Join(dir, "uptime"), "12345.67 45678.90\n")
}

func TestSamplerMeasuresAcrossTheWholeInterval(t *testing.T) {
	dir := t.TempDir()
	s := &Sampler{ProcPath: dir}

	writeProc(t, dir, "cpu  1000 0 0 1000 0 0 0 0 0 0")
	if _, err := s.Sample(); !errors.Is(err, ErrNoBaseline) {
		t.Fatalf("first sample: err = %v, want ErrNoBaseline", err)
	}

	// +1000 jiffies, +250 idle → 75% busy over the interval.
	writeProc(t, dir, "cpu  1750 0 0 1250 0 0 0 0 0 0")
	r, err := s.Sample()
	if err != nil {
		t.Fatal(err)
	}
	if r.CPUPercent != 75 {
		t.Errorf("CPUPercent = %v, want 75", r.CPUPercent)
	}
	if r.MemTotalBytes != 8192000*1024 || r.MemUsedBytes != (8192000-2048000)*1024 {
		t.Errorf("mem = %d/%d", r.MemUsedBytes, r.MemTotalBytes)
	}
	if r.Load1 != 0.42 || r.UptimeSeconds != 12345 {
		t.Errorf("load1 = %v uptime = %d", r.Load1, r.UptimeSeconds)
	}
}

// A counter that went backwards must not be reported as an idle host: the sampler re-baselines and
// reports nothing for that tick.
func TestSamplerRebaselinesOnAWrappedCounter(t *testing.T) {
	dir := t.TempDir()
	s := &Sampler{ProcPath: dir}

	writeProc(t, dir, "cpu  9000 0 0 1000 0 0 0 0 0 0")
	_, _ = s.Sample()
	writeProc(t, dir, "cpu  100 0 0 100 0 0 0 0 0 0")
	if _, err := s.Sample(); !errors.Is(err, ErrNoBaseline) {
		t.Fatalf("after wrap: err = %v, want ErrNoBaseline", err)
	}
	writeProc(t, dir, "cpu  200 0 0 200 0 0 0 0 0 0")
	r, err := s.Sample()
	if err != nil {
		t.Fatal(err)
	}
	if r.CPUPercent != 50 {
		t.Errorf("CPUPercent = %v, want 50", r.CPUPercent)
	}
}

func TestSamplerToleratesMissingExtras(t *testing.T) {
	dir := t.TempDir()
	s := &Sampler{ProcPath: dir}
	mustWrite(t, filepath.Join(dir, "meminfo"), "MemTotal:        8192000 kB\nMemAvailable:    2048000 kB\n")
	mustWrite(t, filepath.Join(dir, "stat"), "cpu  100 0 0 100 0 0 0 0 0 0\n")
	_, _ = s.Sample()
	mustWrite(t, filepath.Join(dir, "stat"), "cpu  200 0 0 200 0 0 0 0 0 0\n")
	r, err := s.Sample()
	if err != nil {
		t.Fatal(err)
	}
	if r.Load1 != 0 || r.UptimeSeconds != 0 || r.MemTotalBytes == 0 {
		t.Errorf("report = %+v", r)
	}
}

func TestReportStats(t *testing.T) {
	st := Report{CPUPercent: 12, MemTotalBytes: 400, MemUsedBytes: 100}.Stats()
	if st.MemPercent != 25 || st.CPUPercent != 12 {
		t.Errorf("stats = %+v", st)
	}
}
