// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

// Package docker's integration ("characterization") suite.
//
// These tests talk to a REAL Docker daemon and lock in the current, observable
// behavior of the internal/docker adapter through its public Client interface —
// nothing here reaches into engineClient internals. They exist so a rewrite of
// this package's guts (such as the docker/docker -> moby/moby SDK swap) can be
// proven not to change behavior: this suite must stay green across it.
//
// They are gated behind the `integration` build tag AND the MIABI_TEST_DOCKER_HOST
// environment variable, so a normal `go test ./...` never runs them. CI runs them
// against a DinD engine matrix (25-28) — see .github/workflows/ci.yml. Run locally
// against your own daemon with:
//
//	MIABI_TEST_DOCKER_HOST=unix:///var/run/docker.sock \
//	    go test -tags integration -race -v ./internal/docker/...
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

// expectNoGoroutineLeak registers a goleak check for the end of the test.
//
// Call it FIRST, before newClient — ordering matters. Cleanups run LIFO, so
// registering the goleak verify first makes it run LAST: after newClient's
// Client.Close (which calls the HTTP transport's CloseIdleConnections) and after
// any container-removal cleanups. That means the Docker SDK's pooled keep-alive
// connections are already being torn down when goleak snapshots, so its built-in
// retry sees them exit — they are not our leak. IgnoreCurrent drops the
// pre-existing baseline, and the persistConn filters cover any pool goroutine
// still winding down. What survives is what we care about: a stream reader that
// ignored context cancellation and kept running.
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

// Images the suite uses. All are tiny and widely mirrored; CI pulls them on
// demand via ensureImage. alpine is the workhorse (has /bin/sh, id, nc, and a
// readable /proc/self/status); alpine/socat backs the DialNetwork relay; postgres
// is the realistic peer for the network-alias and relay tests.
const (
	imgAlpine   = "alpine:3"
	imgSocat    = "alpine/socat:latest"
	imgPostgres = "postgres:17-alpine"
)

// nameSeq makes container/network/volume names unique within a run without
// needing time/rand (which the harness forbids in some contexts). Process id +
// a monotonic counter is enough for a single test binary.
var nameSeq atomic.Uint64

func uniqueName(prefix string) string {
	return fmt.Sprintf("miabi-it-%s-%d-%d", prefix, os.Getpid(), nameSeq.Add(1))
}

// newClient dials the daemon named by MIABI_TEST_DOCKER_HOST, or skips the test
// when it is unset. The client is closed at test end.
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

// ensureImage pulls ref if it is not already present, so the suite is
// self-sufficient on a fresh DinD engine. Exercises ImageExists + PullImage.
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

// run creates+starts a container from spec, ensuring its image is present and
// registering it for forced removal at test end. Returns the container id.
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

// sleeper is a RunSpec for a long-lived alpine container that just idles, tagged
// managed and (unless overridden) resource-capped. Handy base for most tests.
func sleeper(t *testing.T) RunSpec {
	return RunSpec{
		Name:          uniqueName("sleep"),
		Image:         imgAlpine,
		Cmd:           []string{"sleep", "3600"},
		RestartPolicy: "no",
		Labels:        map[string]string{LabelApp: "it"},
	}
}

// execOutput runs argv inside container id over a TTY exec and returns its
// output with trailing CR/LF trimmed. TTY mode gives a raw, un-multiplexed
// stream, so the helper needs no stdcopy demux; it is meant for small state
// probes (id -u, /proc/self/status, getent hosts). The dedicated exec test
// characterizes non-TTY stdcopy framing separately.
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
