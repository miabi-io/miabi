//go:build dockere2e

// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package nodestats

import (
	"context"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
)

type localClients struct{ cli docker.Client }

func (l localClients) For(uint) (docker.Client, error) { return l.cli, nil }

// Behind a build tag: needs a live daemon. It is the only check that binding the daemon's own data
// root and parsing df agree end to end.
//
//	go test -tags dockere2e ./internal/services/nodestats/ -run DataRoot -v
func TestMeasureDataRootOnALiveDaemon(t *testing.T) {
	cli, err := docker.New()
	if err != nil {
		t.Skipf("no docker daemon: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	info, err := cli.Info(ctx)
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if info.DataRoot == "" {
		t.Skip("daemon reports no data root")
	}
	t.Logf("data root: %s (%d cores, %d bytes memory)", info.DataRoot, info.CPUs, info.MemTotal)

	s := NewService(localClients{cli: cli})
	total, free := s.measureDataRoot(ctx, cli, info.DataRoot)
	if total <= 0 {
		t.Fatalf("data root filesystem reported %d bytes — the bind or the df parse is wrong", total)
	}
	if free < 0 || free > total {
		t.Fatalf("free = %d of total %d", free, total)
	}
	t.Logf("node disk: %d bytes free of %d (%.1f%% used)",
		free, total, float64(total-free)/float64(total)*100)
}
