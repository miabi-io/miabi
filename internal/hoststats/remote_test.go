// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package hoststats

import (
	"math"
	"strings"
	"testing"
)

// Verbatim output of SampleCommand from busybox:1.36 on a real host, so the parser is tested
// against what a node actually sends back rather than a hand-written approximation.
const busyboxSample = `cpu  3267192 2 1137424 308550682 41345 0 661844 0 0 0
cpu  3267217 2 1137431 308552158 41345 0 661847 0 0 0
MemTotal:       12304072 kB
MemFree:         6692244 kB
MemAvailable:   10006532 kB
Buffers:             152 kB
Cached:           804816 kB`

func TestParseSampleFromRealBusyboxOutput(t *testing.T) {
	st, err := ParseSample(busyboxSample)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if st.MemTotalBytes != 12304072*1024 {
		t.Fatalf("mem total = %d, want %d", st.MemTotalBytes, 12304072*1024)
	}
	// Used is total minus AVAILABLE, not minus free: page cache is reclaimable and counting it as
	// used would report almost every Linux host as nearly full.
	wantUsed := uint64((12304072 - 10006532) * 1024)
	if st.MemUsedBytes != wantUsed {
		t.Fatalf("mem used = %d, want %d", st.MemUsedBytes, wantUsed)
	}
	if st.MemPercent < 18 || st.MemPercent > 19 {
		t.Fatalf("mem percent = %.2f, want ~18.7", st.MemPercent)
	}
	// Between the two samples: 1508 jiffies total, 1476 of them idle.
	if st.CPUPercent <= 0 || st.CPUPercent > 10 {
		t.Fatalf("cpu percent = %.2f, want a small non-zero value", st.CPUPercent)
	}
}

// A kernel without MemAvailable must still report memory, via MemFree+Buffers+Cached.
func TestParseSampleFallsBackWithoutMemAvailable(t *testing.T) {
	sample := strings.ReplaceAll(busyboxSample, "MemAvailable:   10006532 kB\n", "")
	st, err := ParseSample(sample)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	wantAvail := uint64((6692244 + 152 + 804816) * 1024)
	if st.MemUsedBytes != st.MemTotalBytes-wantAvail {
		t.Fatalf("used = %d, want %d", st.MemUsedBytes, st.MemTotalBytes-wantAvail)
	}
}

// A truncated sample must fail loudly: reporting 0% for a node nobody measured would read as an
// idle node rather than a missing one.
func TestParseSampleRejectsATruncatedSample(t *testing.T) {
	half := "cpu  3267192 2 1137424 308550682 41345 0 661844 0 0 0\nMemTotal: 12304072 kB"
	if _, err := ParseSample(half); err == nil {
		t.Fatal("expected an error for a sample with one cpu line")
	}
	if _, err := ParseSample(""); err == nil {
		t.Fatal("expected an error for empty output")
	}
}

func TestParseSampleRejectsMissingMemTotal(t *testing.T) {
	noMem := "cpu  1 0 1 10 0 0 0 0 0 0\ncpu  2 0 1 20 0 0 0 0 0 0\n"
	if _, err := ParseSample(noMem); err == nil {
		t.Fatal("expected an error when MemTotal is absent")
	}
}

// An idle host between samples reports 0%, not a negative or NaN value.
func TestParseSampleIdleHost(t *testing.T) {
	idle := "cpu  100 0 100 1000 0 0 0 0 0 0\ncpu  100 0 100 1100 0 0 0 0 0 0\nMemTotal: 1024 kB\nMemAvailable: 512 kB"
	st, err := ParseSample(idle)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if st.CPUPercent != 0 || math.IsNaN(st.CPUPercent) {
		t.Fatalf("cpu percent = %v, want 0", st.CPUPercent)
	}
	if st.MemPercent != 50 {
		t.Fatalf("mem percent = %v, want 50", st.MemPercent)
	}
}
