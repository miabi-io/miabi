// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package nodes

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
)

// stubClient is a no-op docker.Client used to populate the registry in tests.
type stubClient struct{ docker.Client }

func (s *stubClient) Close() error { return nil }

// pingClient is a docker.Client whose Ping returns a fixed result, for probing.
type pingClient struct {
	docker.Client
	err error
}

func (p *pingClient) Ping(context.Context) error { return p.err }
func (p *pingClient) Close() error               { return nil }

func TestClientsResolution(t *testing.T) {
	local := &stubClient{}
	c := NewClients(1, local)

	// Local id and the zero id both resolve to the local client.
	for _, id := range []uint{1, 0} {
		got, err := c.For(id)
		if err != nil || got != docker.Client(local) {
			t.Errorf("For(%d) = (%v, %v), want local", id, got, err)
		}
		if !c.IsLocal(id) || !c.Connected(id) {
			t.Errorf("id %d should be local+connected", id)
		}
	}

	// Unknown remote node is offline.
	if _, err := c.For(2); !errors.Is(err, ErrNodeOffline) {
		t.Errorf("For(2) err = %v, want ErrNodeOffline", err)
	}
	if c.Connected(2) {
		t.Error("node 2 should not be connected")
	}

	// Register, resolve, then remove a remote node.
	remote := &stubClient{}
	c.SetRemote(2, remote)
	if got, err := c.For(2); err != nil || got != docker.Client(remote) {
		t.Errorf("For(2) after SetRemote = (%v, %v), want remote", got, err)
	}
	if !c.Connected(2) {
		t.Error("node 2 should be connected after SetRemote")
	}
	c.RemoveRemote(2)
	if _, err := c.For(2); !errors.Is(err, ErrNodeOffline) {
		t.Errorf("For(2) after RemoveRemote err = %v, want ErrNodeOffline", err)
	}
}

func TestClientsRemoteIDs(t *testing.T) {
	c := NewClients(1, &stubClient{})
	if len(c.RemoteIDs()) != 0 {
		t.Fatal("no remotes yet")
	}
	c.SetRemote(2, &stubClient{})
	c.SetRemote(3, &stubClient{})
	ids := c.RemoteIDs()
	if len(ids) != 2 {
		t.Fatalf("RemoteIDs = %v, want 2", ids)
	}
	for _, id := range ids {
		if id == 1 {
			t.Error("local id must not appear in RemoteIDs")
		}
	}
	c.RemoveRemote(2)
	if len(c.RemoteIDs()) != 1 {
		t.Errorf("after RemoveRemote, RemoteIDs = %v, want 1", c.RemoteIDs())
	}
}

func TestManagerProbe(t *testing.T) {
	c := NewClients(1, &stubClient{})
	c.SetRemote(2, &pingClient{err: nil})                       // healthy
	c.SetRemote(3, &pingClient{err: errors.New("tunnel dead")}) // dead
	m := NewManager(c, nil)                                     // node service unused by probe

	if err := m.probe(context.Background(), 2); err != nil {
		t.Errorf("healthy probe = %v, want nil", err)
	}
	if err := m.probe(context.Background(), 3); err == nil {
		t.Error("dead node probe should fail")
	}
	if err := m.probe(context.Background(), 99); !errors.Is(err, ErrNodeOffline) {
		t.Errorf("offline node probe err = %v, want ErrNodeOffline", err)
	}
}

// taskClient reports the swarm task containers this engine can see. An engine that
// isn't running the task returns ErrNotFound — which is exactly what the manager
// does for a task the scheduler placed on a worker.
type taskClient struct {
	docker.Client
	containerID string // "" = this engine is not running the task
	// runningTasks is what the MANAGER's swarm view reports. It is what tells a
	// running-but-unreachable task apart from nothing running at all.
	runningTasks uint64
}

func (t *taskClient) ServiceTaskContainerID(context.Context, string) (string, error) {
	if t.containerID == "" {
		return "", docker.ErrNotFound
	}
	return t.containerID, nil
}

func (t *taskClient) ServiceInspect(context.Context, string) (docker.ServiceStatus, error) {
	return docker.ServiceStatus{RunningTasks: t.runningTasks}, nil
}

// Regression: a single-replica service placed on a worker. The manager can list the
// task but cannot see its container, so resolving through the manager returned
// "no active container" and the app showed no logs, no metrics, and no uptime —
// while `docker service ls` happily reported 1/1. Only the node running the task
// can see it, so the registry must find that node.
func TestForServiceTaskFindsATaskOnARemoteNode(t *testing.T) {
	manager := &taskClient{}                          // not running the task
	node1 := &taskClient{containerID: "1ab857b7d825"} // running it
	c := NewClients(1, manager)
	c.SetRemote(2, node1)

	dc, cid, err := c.ForServiceTask(context.Background(), "mb-app-hefpkzz4-6")
	if err != nil {
		t.Fatalf("service task on a worker was not found: %v", err)
	}
	if cid != "1ab857b7d825" {
		t.Errorf("container id = %q, want the one on node1", cid)
	}
	if dc != docker.Client(node1) {
		t.Error("resolved the manager's engine, which cannot read a container on node1")
	}
}

// The manager wins when it is running the task (single-node, or a replica landed
// there) — that path must stay free of remote round-trips.
func TestForServiceTaskPrefersTheLocalEngine(t *testing.T) {
	manager := &taskClient{containerID: "local-abc"}
	c := NewClients(1, manager)
	c.SetRemote(2, &taskClient{containerID: "remote-xyz"})

	dc, cid, err := c.ForServiceTask(context.Background(), "mb-app-1")
	if err != nil {
		t.Fatalf("ForServiceTask: %v", err)
	}
	if cid != "local-abc" || dc != docker.Client(manager) {
		t.Errorf("got %q on a remote engine; want the local task", cid)
	}
}

// Nothing is running (the service is scaled to zero, or was never deployed): the
// manager reports no tasks, so this is a genuine not-found.
func TestForServiceTaskNotFound(t *testing.T) {
	c := NewClients(1, &taskClient{runningTasks: 0})
	c.SetRemote(2, &taskClient{})
	if _, _, err := c.ForServiceTask(context.Background(), "mb-app-1"); !errors.Is(err, docker.ErrNotFound) {
		t.Fatalf("want docker.ErrNotFound, got %v", err)
	}
}

// The service IS running, but on a swarm node with no Miabi agent — so no engine we
// hold can see its container. That must NOT be reported as "nothing is running":
// the app is healthy, and the honest answer is "we cannot reach it", which is what
// tells the user to install the agent rather than hunt a phantom crash.
func TestForServiceTaskRunningOnAnUnmanagedNode(t *testing.T) {
	// The manager sees no container of its own, but its swarm view says 1 task is up.
	c := NewClients(1, &taskClient{runningTasks: 1})

	_, _, err := c.ForServiceTask(context.Background(), "mb-app-1")
	if !errors.Is(err, ErrTaskUnreachable) {
		t.Fatalf("want ErrTaskUnreachable, got %v", err)
	}
	if errors.Is(err, docker.ErrNotFound) {
		t.Error("a running task must not be reported as not-found — that is what made a " +
			"healthy 1/1 service look like it had no container")
	}
}
