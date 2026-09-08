// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package docker

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// requireSwarmManager ensures the daemon is a reachable swarm manager, or skips.
// If the engine is already an active manager it is used as-is (never torn down).
// Otherwise it initializes a single-node swarm — but only when MIABI_TEST_SWARM=1
// is set, since that mutates global daemon state; the swarm is left on cleanup.
// CI's DinD engines set MIABI_TEST_SWARM=1 (see .github/workflows/ci.yml).
func requireSwarmManager(t *testing.T, cli Client) SwarmInfo {
	t.Helper()
	ctx := bg(t, 30*time.Second)
	info, err := cli.Swarm(ctx)
	if err != nil {
		t.Fatalf("Swarm: %v", err)
	}
	if info.LocalNodeState == "active" && info.ControlAvailable {
		return info
	}
	if os.Getenv("MIABI_TEST_SWARM") != "1" {
		t.Skip("swarm: set MIABI_TEST_SWARM=1 to run (initializes a single-node swarm on the target daemon)")
	}
	if _, err := cli.SwarmInit(ctx, SwarmInitRequest{AdvertiseAddr: "127.0.0.1"}); err != nil {
		t.Fatalf("SwarmInit: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = cli.SwarmLeave(c, true)
	})
	info, err = cli.Swarm(ctx)
	if err != nil {
		t.Fatalf("Swarm after init: %v", err)
	}
	return info
}

// TestSwarmInfoAndNodes covers the read-only swarm surface: state, node id, and
// that the manager sees itself as a node. Works on any single-node swarm.
func TestSwarmInfoAndNodes(t *testing.T) {
	cli := newClient(t)
	info := requireSwarmManager(t, cli)
	ctx := bg(t, 30*time.Second)

	if info.NodeID == "" {
		t.Error("SwarmInfo.NodeID is empty on an active manager")
	}
	if !info.ControlAvailable {
		t.Error("SwarmInfo.ControlAvailable is false on a manager")
	}

	nodes, err := cli.SwarmNodes(ctx)
	if err != nil {
		t.Fatalf("SwarmNodes: %v", err)
	}
	if len(nodes) < 1 {
		t.Fatalf("SwarmNodes returned %d nodes, want >= 1", len(nodes))
	}

	toks, err := cli.SwarmJoinTokens(ctx)
	if err != nil {
		t.Fatalf("SwarmJoinTokens: %v", err)
	}
	if toks.Worker == "" || toks.Manager == "" {
		t.Errorf("SwarmJoinTokens incomplete: worker=%q manager=%q", toks.Worker, toks.Manager)
	}
}

// TestSwarmServiceLifecycle is the safety net at single-node scale: it
// creates a replicated service, scales it, updates it, reads its logs FROM THE
// MANAGER, restarts it, and removes it — the full cluster-app path that cluster
// deploys use instead of RunContainer. Multi-node placement is exercised by the
// DinD swarm in CI; the call surface is identical.
func TestSwarmServiceLifecycle(t *testing.T) {
	cli := newClient(t)
	requireSwarmManager(t, cli)
	ensureImage(t, cli, imgAlpine)
	ctx := bg(t, 3*time.Minute)

	name := uniqueName("svc")
	overlay := uniqueName("ovl")
	if _, err := cli.CreateOverlayNetwork(ctx, overlay); err != nil {
		t.Fatalf("CreateOverlayNetwork: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = cli.RemoveNetwork(c, overlay)
	})

	id, err := cli.ServiceCreate(ctx, ServiceSpec{
		Name:           name,
		Image:          imgAlpine,
		Cmd:            []string{"sh", "-c", "i=0; while true; do echo svc-line-$i; i=$((i+1)); sleep 0.5; done"},
		Replicas:       1,
		Networks:       []string{overlay},
		NetworkAliases: []string{"svc"},
		Labels:         map[string]string{LabelApp: "swarm-it"},
	})
	if err != nil {
		t.Fatalf("ServiceCreate: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = cli.ServiceRemove(c, id)
	})

	// Wait for the single replica to be running.
	if !waitRunningTasks(t, cli, name, 1, 90*time.Second) {
		t.Fatal("service task did not reach running (1 replica)")
	}

	// It shows up in the list.
	svcs, err := cli.ServiceList(ctx)
	if err != nil {
		t.Fatalf("ServiceList: %v", err)
	}
	if !hasService(svcs, name) {
		t.Errorf("service %q not in ServiceList", name)
	}

	// Scale to 2 desired replicas.
	if err := cli.ServiceScale(ctx, name, 2); err != nil {
		t.Fatalf("ServiceScale: %v", err)
	}
	if st, err := cli.ServiceInspect(ctx, name); err != nil {
		t.Fatalf("ServiceInspect after scale: %v", err)
	} else if st.Replicas != 2 {
		t.Errorf("desired replicas after scale: got %d, want 2", st.Replicas)
	}

	// Update env (rolling). Must not error and must keep the service present.
	if err := cli.ServiceUpdate(ctx, name, ServiceSpec{
		Name:     name,
		Image:    imgAlpine,
		Cmd:      []string{"sh", "-c", "i=0; while true; do echo svc-line-$i; i=$((i+1)); sleep 0.5; done"},
		Replicas: 2,
		Env:      []string{"MIABI_IT=updated"},
		Networks: []string{overlay},
		Labels:   map[string]string{LabelApp: "swarm-it"},
	}); err != nil {
		t.Fatalf("ServiceUpdate: %v", err)
	}

	// Read service logs from the manager (aggregated across tasks).
	logCtx, cancelLogs := context.WithTimeout(ctx, 30*time.Second)
	var sawLog bool
	err = cli.StreamServiceLogs(logCtx, name, true, "10", func(l LogLine) error {
		if strings.Contains(l.Text, "svc-line-") {
			sawLog = true
			cancelLogs()
		}
		return nil
	})
	cancelLogs()
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("StreamServiceLogs: %v", err)
	}
	if !sawLog {
		t.Error("StreamServiceLogs (from manager) yielded no 'svc-line-' output")
	}

	// Force a rolling restart in place.
	if err := cli.ServiceRestart(ctx, name); err != nil {
		t.Fatalf("ServiceRestart: %v", err)
	}

	// Remove and confirm it is gone. NOTE: ServiceInspect returns the raw SDK
	// not-found error here — it does NOT normalize to ErrNotFound the way
	// InspectContainer/CopyFileFromVolume do. That asymmetry is current behavior;
	// this suite locks it in (asserting a non-nil error, not ErrNotFound) so the
	// migration preserves it rather than silently changing the error surface.
	if err := cli.ServiceRemove(ctx, name); err != nil {
		t.Fatalf("ServiceRemove: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := cli.ServiceInspect(ctx, name); err != nil {
			return // gone (raw not-found error)
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Errorf("ServiceInspect kept succeeding after ServiceRemove")
}

func waitRunningTasks(t *testing.T, cli Client, name string, want uint64, d time.Duration) bool {
	t.Helper()
	ctx := bg(t, d+10*time.Second)
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		st, err := cli.ServiceInspect(ctx, name)
		if err == nil && st.RunningTasks >= want {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func hasService(svcs []ServiceStatus, name string) bool {
	for _, s := range svcs {
		if s.Name == name {
			return true
		}
	}
	return false
}
