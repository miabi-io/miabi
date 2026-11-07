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

package hoststats_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/hoststats"
)

// Needs a live Docker daemon: go test -tags dockere2e ./internal/hoststats/ -run SampleOnALiveDaemon
func TestSampleOnALiveDaemon(t *testing.T) {
	cli, err := docker.New()
	if err != nil {
		t.Skipf("no docker daemon: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	const image = "busybox:1.36"
	if err := cli.PullImage(ctx, image, nil); err != nil {
		t.Skipf("cannot pull %s: %v", image, err)
	}
	exit, out, err := cli.RunOneShot(ctx, docker.RunSpec{
		Name:       fmt.Sprintf("mb-hoststats-e2e-%d", time.Now().UnixNano()),
		Image:      image,
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{hoststats.SampleCommand[2]},
	})
	if err != nil {
		t.Fatalf("run: %v (out=%q)", err, out)
	}
	if exit != 0 {
		t.Fatalf("helper exited %d: %s", exit, out)
	}
	st, err := hoststats.ParseSample(out)
	if err != nil {
		t.Fatalf("parse: %v (raw=%q)", err, out)
	}
	if st.MemTotalBytes == 0 {
		t.Fatal("no memory reported — a container's /proc should be the host's")
	}
	if st.CPUPercent < 0 || st.CPUPercent > 100 {
		t.Fatalf("cpu percent out of range: %v", st.CPUPercent)
	}
	t.Logf("sampled host: cpu=%.1f%% mem=%.1f%% (%d of %d bytes)",
		st.CPUPercent, st.MemPercent, st.MemUsedBytes, st.MemTotalBytes)
}
