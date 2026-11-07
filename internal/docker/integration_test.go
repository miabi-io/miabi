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

//go:build integration

// Integration suite against a real daemon; needs the integration tag and MIABI_TEST_DOCKER_HOST.
package docker

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// Call first: cleanups run LIFO, so goleak verifies after the client closes its pooled connections.
func expectNoGoroutineLeak(t *testing.T) {
	t.Helper()
	opts := []goleak.Option{
		goleak.IgnoreCurrent(),
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).writeLoop"),
	}
	t.Cleanup(func() { goleak.VerifyNone(t, opts...) })
}

const (
	imgAlpine   = "alpine:3"
	imgSocat    = "alpine/socat:latest"
	imgPostgres = "postgres:17-alpine"
)

var nameSeq atomic.Uint64

func uniqueName(prefix string) string {
	return fmt.Sprintf("miabi-it-%s-%d-%d", prefix, os.Getpid(), nameSeq.Add(1))
}

func newClient(t *testing.T) Client {
	t.Helper()
	host := os.Getenv("MIABI_TEST_DOCKER_HOST")
	if host == "" {
		t.Skip("integration: set MIABI_TEST_DOCKER_HOST (e.g. unix:///var/run/docker.sock) to run")
	}
	cli, err := NewSocket(host)
	if err != nil {
		t.Fatalf("NewSocket(%q): %v", host, err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := cli.Ping(ctx); err != nil {
		t.Fatalf("Ping: daemon at %q not reachable: %v", host, err)
	}
	return cli
}

// bg returns a context with a generous timeout and registers its cancel.
func bg(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func ensureImage(t *testing.T, cli Client, ref string) {
	t.Helper()
	ctx := bg(t, 3*time.Minute)
	ok, err := cli.ImageExists(ctx, ref)
	if err != nil {
		t.Fatalf("ImageExists(%q): %v", ref, err)
	}
	if ok {
		return
	}
	if err := cli.PullImage(ctx, ref, nil); err != nil {
		t.Fatalf("PullImage(%q): %v", ref, err)
	}
}

func run(t *testing.T, cli Client, spec RunSpec) string {
	t.Helper()
	ensureImage(t, cli, spec.Image)
	if spec.Name == "" {
		spec.Name = uniqueName("c")
	}
	ctx := bg(t, 30*time.Second)
	id, err := cli.RunContainer(ctx, spec)
	if id != "" {
		t.Cleanup(func() {
			rmCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_ = cli.RemoveContainer(rmCtx, id, true)
		})
	}
	if err != nil {
		t.Fatalf("RunContainer(%s): %v", spec.Name, err)
	}
	return id
}

func sleeper(t *testing.T) RunSpec {
	return RunSpec{
		Name:          uniqueName("sleep"),
		Image:         imgAlpine,
		Cmd:           []string{"sleep", "3600"},
		RestartPolicy: "no",
		Labels:        map[string]string{LabelApp: "it"},
	}
}

// TTY mode gives a raw stream, so no stdcopy demux is needed; meant for small state probes.
func execOutput(t *testing.T, cli Client, id string, argv ...string) string {
	t.Helper()
	ctx := bg(t, 30*time.Second)
	st, err := cli.Exec(ctx, id, ExecOptions{Cmd: argv, Tty: true})
	if err != nil {
		t.Fatalf("Exec(%v): %v", argv, err)
	}
	defer st.Close()
	var out strings.Builder
	buf := make([]byte, 4096)
	for {
		n, rerr := st.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
		}
		if rerr != nil {
			break
		}
	}
	return strings.TrimRight(out.String(), "\r\n")
}
