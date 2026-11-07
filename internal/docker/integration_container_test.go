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

package docker

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// A wrong zero value silently ships an uncapped or over-privileged container that create would not reject.
func TestContainerCreateInspect(t *testing.T) {
	cli := newClient(t)
	const (
		wantMem = int64(64 * 1024 * 1024) // 64Mi
		wantCPU = int64(500_000_000)      // 0.5 CPU in NanoCPUs
	)
	id := run(t, cli, RunSpec{
		Image:           imgAlpine,
		Cmd:             []string{"sleep", "3600"},
		MemoryBytes:     wantMem,
		NanoCPUs:        wantCPU,
		RestartPolicy:   "on-failure:3",
		User:            "1000:1000",
		NoNewPrivileges: true,
		CapDrop:         []string{"NET_RAW"},
		Labels:          map[string]string{LabelApp: "it"},
	})

	ctx := bg(t, 20*time.Second)
	cfg, err := cli.InspectContainerConfig(ctx, id)
	if err != nil {
		t.Fatalf("InspectContainerConfig: %v", err)
	}
	if cfg.MemoryBytes != wantMem {
		t.Errorf("memory cap: got %d, want %d", cfg.MemoryBytes, wantMem)
	}
	if cfg.NanoCPUs != wantCPU {
		t.Errorf("nano cpus: got %d, want %d", cfg.NanoCPUs, wantCPU)
	}
	if cfg.RestartPolicy != "on-failure:3" {
		t.Errorf("restart policy: got %q, want %q", cfg.RestartPolicy, "on-failure:3")
	}

	// The managed label is applied by RunContainer unconditionally.
	if got := cfg.Labels[LabelManaged]; got != "true" {
		t.Errorf("managed label: got %q, want \"true\"", got)
	}

	// Observe the security profile from inside the container.
	if uid := execOutput(t, cli, id, "id", "-u"); uid != "1000" {
		t.Errorf("running uid: got %q, want \"1000\" (non-root user not applied)", uid)
	}
	// no-new-privileges is visible as NoNewPrivs: 1 in /proc/self/status.
	status := execOutput(t, cli, id, "cat", "/proc/self/status")
	if !strings.Contains(status, "NoNewPrivs:\t1") {
		t.Errorf("no-new-privileges not set; /proc/self/status had:\n%s", firstLines(status, 40))
	}
}

func TestContainerLifecycle(t *testing.T) {
	cli := newClient(t)
	id := run(t, cli, sleeper(t))
	ctx := bg(t, 60*time.Second)

	c, err := cli.InspectContainer(ctx, id)
	if err != nil {
		t.Fatalf("InspectContainer: %v", err)
	}
	if c.State != "running" {
		t.Fatalf("state after run: got %q, want running", c.State)
	}

	if err := cli.StopContainer(ctx, id, 5); err != nil {
		t.Fatalf("StopContainer: %v", err)
	}
	if c, _ := cli.InspectContainer(ctx, id); c.State == "running" {
		t.Errorf("state after stop: still running")
	}
	if err := cli.StartContainer(ctx, id); err != nil {
		t.Fatalf("StartContainer: %v", err)
	}
	if err := cli.RestartContainer(ctx, id, 5); err != nil {
		t.Fatalf("RestartContainer: %v", err)
	}
	if err := cli.RemoveContainer(ctx, id, true); err != nil {
		t.Fatalf("RemoveContainer: %v", err)
	}
	if _, err := cli.InspectContainer(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("InspectContainer after remove: got %v, want ErrNotFound", err)
	}
}

func TestLogsNonTTYFraming(t *testing.T) {
	cli := newClient(t)
	id := run(t, cli, RunSpec{
		Image:         imgAlpine,
		Cmd:           []string{"sh", "-c", "echo to-stdout; echo to-stderr 1>&2; sleep 1"},
		RestartPolicy: "no",
		Labels:        map[string]string{LabelApp: "it"},
	})

	ctx := bg(t, 30*time.Second)
	var mu sync.Mutex
	byStream := map[string][]string{}
	err := cli.StreamLogs(ctx, id, false, "all", func(l LogLine) error {
		mu.Lock()
		defer mu.Unlock()
		byStream[l.Stream] = append(byStream[l.Stream], strings.TrimSpace(l.Text))
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("StreamLogs: %v", err)
	}
	if !contains(byStream["stdout"], "to-stdout") {
		t.Errorf("stdout stream missing 'to-stdout'; got %v", byStream)
	}
	if !contains(byStream["stderr"], "to-stderr") {
		t.Errorf("stderr stream missing 'to-stderr' (framing collapsed?); got %v", byStream)
	}
}

// A follow that ignores ctx would pass every functional assertion and leak forever.
func TestLogsFollowCancelNoLeak(t *testing.T) {
	expectNoGoroutineLeak(t)
	cli := newClient(t)
	id := run(t, cli, RunSpec{
		Image:         imgAlpine,
		Cmd:           []string{"sh", "-c", "i=0; while true; do echo line-$i; i=$((i+1)); sleep 0.05; done"},
		RestartPolicy: "no",
		Labels:        map[string]string{LabelApp: "it"},
	})

	ctx, cancel := context.WithCancel(context.Background())
	var seen int
	done := make(chan error, 1)
	go func() {
		done <- cli.StreamLogs(ctx, id, true, "0", func(l LogLine) error {
			seen++
			if seen >= 3 {
				cancel() // stop following mid-stream
			}
			return nil
		})
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("StreamLogs(follow) did not return after cancel")
	}
	if seen < 3 {
		t.Fatalf("expected to observe at least 3 log lines before cancel, got %d", seen)
	}
	// Give the SDK reader goroutine a moment to unwind before goleak snapshots.
	time.Sleep(200 * time.Millisecond)
}

func TestExecNonTTYAndExitCode(t *testing.T) {
	cli := newClient(t)
	id := run(t, cli, sleeper(t))
	ctx := bg(t, 30*time.Second)

	// Non-TTY output is stdcopy-framed: first byte is the stream type, bytes 1..3 are zero padding.
	st, err := cli.Exec(ctx, id, ExecOptions{Cmd: []string{"echo", "hello-exec"}, Tty: false})
	if err != nil {
		t.Fatalf("Exec non-tty: %v", err)
	}
	raw, _ := io.ReadAll(readerOf(st))
	st.Close()
	if len(raw) < 8 {
		t.Fatalf("non-tty exec produced %d bytes, expected an 8-byte stdcopy header + payload", len(raw))
	}
	if raw[0] != 1 || raw[1] != 0 || raw[2] != 0 || raw[3] != 0 {
		t.Errorf("expected stdcopy stdout frame header (1,0,0,0), got %v", raw[:4])
	}
	if !strings.Contains(string(raw[8:]), "hello-exec") {
		t.Errorf("non-tty exec payload missing output; frame body=%q", string(raw[8:]))
	}

	// The command sleeps so the exec is still alive when Resize runs; resizing a finished exec errors.
	tst, err := cli.Exec(ctx, id, ExecOptions{Cmd: []string{"sh", "-c", "sleep 1; echo hello-tty"}, Tty: true})
	if err != nil {
		t.Fatalf("Exec tty: %v", err)
	}
	if err := tst.Resize(ctx, 40, 120); err != nil {
		t.Errorf("Resize: %v", err)
	}
	body, _ := io.ReadAll(readerOf(tst))
	tst.Close()
	if !strings.Contains(string(body), "hello-tty") {
		t.Errorf("tty exec output missing; got %q", string(body))
	}
}

func TestStatsBusyVsIdle(t *testing.T) {
	cli := newClient(t)
	busy := run(t, cli, RunSpec{
		Image:         imgAlpine,
		Cmd:           []string{"sh", "-c", "while true; do :; done"},
		RestartPolicy: "no",
		Labels:        map[string]string{LabelApp: "it"},
	})
	idle := run(t, cli, sleeper(t))
	ctx := bg(t, 30*time.Second)

	// Let the busy loop accumulate CPU across a sampling interval.
	time.Sleep(1500 * time.Millisecond)

	bs, err := cli.StatsOnce(ctx, busy)
	if err != nil {
		t.Fatalf("StatsOnce(busy): %v", err)
	}
	is, err := cli.StatsOnce(ctx, idle)
	if err != nil {
		t.Fatalf("StatsOnce(idle): %v", err)
	}
	if bs.CPUPercent <= 0 {
		t.Errorf("busy container CPU%%: got %.3f, want > 0", bs.CPUPercent)
	}
	if bs.MemoryUsage == 0 {
		t.Errorf("busy container memory usage reported as 0 bytes")
	}
	if is.CPUPercent > 5 {
		t.Errorf("idle container CPU%%: got %.3f, want near 0", is.CPUPercent)
	}
}

// readerOf adapts an ExecStream to an io.Reader for io.ReadAll.
func readerOf(s ExecStream) io.Reader { return execReader{s} }

type execReader struct{ s ExecStream }

func (r execReader) Read(p []byte) (int, error) { return r.s.Read(p) }

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
