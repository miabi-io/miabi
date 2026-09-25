// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package nodestats

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/pkg/hoststats"
)

const gib = uint64(1) << 30

var errNoDocker = errors.New("no docker for this node")

type noDocker struct{ calls int }

func (n *noDocker) For(uint) (docker.Client, error) {
	n.calls++
	return nil, errNoDocker
}

type usageWrite struct {
	id  uint
	cpu float64
	mem int64
	at  time.Time
}

type fakeServers struct{ writes []usageWrite }

func (f *fakeServers) List() ([]models.Server, error) { return nil, nil }
func (f *fakeServers) SetCapacity(uint, int, int64, int64, int64, time.Time) error {
	return nil
}
func (f *fakeServers) SetUsage(id uint, cpu float64, mem int64, at time.Time) error {
	f.writes = append(f.writes, usageWrite{id, cpu, mem, at})
	return nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestService() (*Service, *noDocker, *fakeServers, *clock) {
	nd := &noDocker{}
	fs := &fakeServers{}
	clk := &clock{t: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
	s := NewService(nd)
	s.SetServers(fs)
	s.now = clk.now
	return s, nd, fs, clk
}

func node() *models.Server {
	return &models.Server{ID: 7, Name: "edge-38", MemoryBytes: int64(8 * gib)}
}

func report(cpu float64, used uint64) hoststats.Report {
	return hoststats.Report{CPUPercent: cpu, MemTotalBytes: 8 * gib, MemUsedBytes: used, Load1: 0.5, UptimeSeconds: 100}
}

func TestValidateReport(t *testing.T) {
	cases := []struct {
		name string
		r    hoststats.Report
		ok   bool
	}{
		{"ordinary", report(42, 2*gib), true},
		{"idle and empty", hoststats.Report{MemTotalBytes: 1}, true},
		{"fully busy", report(100, 8*gib), true},
		{"negative cpu", report(-1, gib), false},
		{"cpu over 100", report(100.5, gib), false},
		{"nan cpu", report(math.NaN(), gib), false},
		{"zero total", hoststats.Report{CPUPercent: 5}, false},
		{"used over total", report(5, 9*gib), false},
		{"negative load", hoststats.Report{CPUPercent: 5, MemTotalBytes: 1, Load1: -1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateReport(tc.r)
			if (err == nil) != tc.ok {
				t.Fatalf("ValidateReport = %v, want ok=%v", err, tc.ok)
			}
			if err != nil && !errors.Is(err, ErrInvalidReport) {
				t.Fatalf("error %v does not wrap ErrInvalidReport", err)
			}
		})
	}
}

func TestIngestRejectsWithoutRecording(t *testing.T) {
	s, _, fs, _ := newTestService()
	if err := s.Ingest(node(), hoststats.Report{CPUPercent: 5}); !errors.Is(err, ErrInvalidReport) {
		t.Fatalf("err = %v", err)
	}
	if s.HasFreshPush(7) || len(fs.writes) != 0 {
		t.Fatal("a rejected report must leave the node unmeasured")
	}
}

func TestGetPrefersAFreshPushAndFallsBackWhenStale(t *testing.T) {
	s, nd, _, clk := newTestService()
	if err := s.Ingest(node(), report(30, 2*gib)); err != nil {
		t.Fatal(err)
	}

	clk.advance(pushStaleAfter - time.Second)
	got, err := s.Get(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceAgent || got.CPUPercent != 30 || !got.DescribesNode || got.Load1 != 0.5 {
		t.Fatalf("sample = %+v", got)
	}
	if nd.calls != 0 {
		t.Fatal("a fresh push must not reach the node")
	}

	clk.advance(time.Second)
	if s.HasFreshPush(7) {
		t.Fatal("a push at the staleness boundary is stale")
	}
	if _, err := s.Get(context.Background(), 7); !errors.Is(err, errNoDocker) {
		t.Fatalf("stale push: err = %v, want the container path's error", err)
	}
	if nd.calls != 1 {
		t.Fatalf("container path calls = %d, want 1", nd.calls)
	}
}

func TestKillSwitchIgnoresPushes(t *testing.T) {
	s, _, fs, _ := newTestService()
	s.SetAgentStats(false)
	if err := s.Ingest(node(), report(30, 2*gib)); err != nil {
		t.Fatal(err)
	}
	if s.HasFreshPush(7) || len(fs.writes) != 0 {
		t.Fatal("with agent stats off a push must change nothing")
	}
}

func TestIngestPersistsOncePerMinuteOrOnDrift(t *testing.T) {
	s, _, fs, clk := newTestService()
	start := clk.t

	push := func(cpu float64, used uint64) {
		t.Helper()
		if err := s.Ingest(node(), report(cpu, used)); err != nil {
			t.Fatal(err)
		}
	}
	push(30, 2*gib)
	clk.advance(15 * time.Second)
	push(32, 2*gib)
	clk.advance(15 * time.Second)
	push(31, 2*gib+gib/100)
	if len(fs.writes) != 1 {
		t.Fatalf("writes = %d after steady pushes, want 1", len(fs.writes))
	}
	if !fs.writes[0].at.Equal(start) {
		t.Fatalf("usage stamped %v, want receipt time %v", fs.writes[0].at, start)
	}

	clk.advance(15 * time.Second)
	push(55, 2*gib)
	if len(fs.writes) != 2 {
		t.Fatalf("a 24-point CPU jump must be written at once, writes = %d", len(fs.writes))
	}
	clk.advance(15 * time.Second)
	push(55, 3*gib)
	if len(fs.writes) != 3 {
		t.Fatalf("a 50%% memory jump must be written at once, writes = %d", len(fs.writes))
	}

	clk.advance(persistEvery)
	push(55, 3*gib)
	if len(fs.writes) != 4 {
		t.Fatalf("a minute on, the figure must be refreshed, writes = %d", len(fs.writes))
	}
}

// A node that is itself a container reports its physical host: shown on its page, never summed.
func TestIngestDoesNotPersistAReadingOfAnotherMachine(t *testing.T) {
	s, _, fs, _ := newTestService()
	srv := node()
	srv.MemoryBytes = int64(2 * gib)
	if err := s.Ingest(srv, report(30, 4*gib)); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Cached(7)
	if !ok || got.DescribesNode {
		t.Fatalf("sample = %+v ok=%v, want a physical-host reading", got, ok)
	}
	if len(fs.writes) != 0 {
		t.Fatal("a physical-host reading must not be written to the node's usage")
	}
}
