//go:build dockere2e

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
	"context"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
)

type localClients struct{ cli docker.Client }

func (l localClients) For(uint) (docker.Client, error) { return l.cli, nil }

// Needs a live daemon: go test -tags dockere2e ./internal/services/nodestats/ -run DataRoot -v
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
