// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package nodestats

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
)

// infoOnly answers Info and nothing else: any other call would panic, which is the assertion that
// the local node is measured without starting a container.
type infoOnly struct {
	docker.Client
	memTotal int64
}

func (f infoOnly) Info(context.Context) (docker.Info, error) {
	return docker.Info{MemTotal: f.memTotal}, nil
}

type localClients struct{ dc docker.Client }

func (l localClients) For(uint) (docker.Client, error) { return l.dc, nil }
func (l localClients) IsLocal(id uint) bool            { return id == 1 }

func fakeHostProc(t *testing.T, memTotalKB string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"stat":    "cpu  100 0 100 800 0 0 0 0 0 0\n",
		"meminfo": "MemTotal:        " + memTotalKB + " kB\nMemAvailable:    1048576 kB\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLocalNodeIsReadFromProcfsNotAContainer(t *testing.T) {
	s := NewService(localClients{dc: infoOnly{memTotal: 2 << 30}})
	s.SetHostProc(fakeHostProc(t, "2097152"))

	got, err := s.Get(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceLocal || !got.DescribesNode || got.MemTotalBytes != 2<<30 {
		t.Fatalf("sample = %+v", got)
	}
	if got, _ := s.Cached(1); got.Source != SourceLocal {
		t.Fatalf("cached = %+v", got)
	}
}

// The configured bind is the host's view; when it disagrees with Docker the reading is still flagged,
// exactly as for a remote node.
func TestLocalNodeReadingOfAnotherMachineIsFlagged(t *testing.T) {
	s := NewService(localClients{dc: infoOnly{memTotal: 2 << 30}})
	s.SetHostProc(fakeHostProc(t, "96461444"))

	got, err := s.Get(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.DescribesNode {
		t.Fatalf("a 96 GB reading on a 2 GiB node must not describe it: %+v", got)
	}
}

func TestLocalEntriesExpireSooner(t *testing.T) {
	if (entry{sample: Sample{Source: SourceLocal}}).ttl() != localTTL {
		t.Fatal("local readings should use the short ttl")
	}
	if (entry{sample: Sample{Source: SourceSampled}}).ttl() != ttl {
		t.Fatal("container samples keep the long ttl")
	}
}
