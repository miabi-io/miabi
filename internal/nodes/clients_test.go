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

package nodes

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
)

type stubClient struct{ docker.Client }

func (s *stubClient) Close() error { return nil }

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

// An engine not running the task returns ErrNotFound, exactly as the manager does for a task on a worker.
type taskClient struct {
	docker.Client
	containerID string // "" = this engine is not running the task
	// The manager's swarm view distinguishes running-but-unreachable from nothing running.
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

// Regression: a 1/1 service on a worker showed no logs or metrics because the manager cannot see its container.
func TestForServiceTaskFindsATaskOnARemoteNode(t *testing.T) {
	manager := &taskClient{}                          // not running the task
	node1 := &taskClient{containerID: "1ab857b7d825"} // running it
	c := NewClients(1, manager)
	c.SetRemote(2, node1)

	dc, cid, err := c.ForServiceTask(context.Background(), 0, "mb-app-hefpkzz4-6")
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

// The local engine must win when it runs the task, with no remote round-trips.
func TestForServiceTaskPrefersTheLocalEngine(t *testing.T) {
	manager := &taskClient{containerID: "local-abc"}
	c := NewClients(1, manager)
	c.SetRemote(2, &taskClient{containerID: "remote-xyz"})

	dc, cid, err := c.ForServiceTask(context.Background(), 0, "mb-app-1")
	if err != nil {
		t.Fatalf("ForServiceTask: %v", err)
	}
	if cid != "local-abc" || dc != docker.Client(manager) {
		t.Errorf("got %q on a remote engine; want the local task", cid)
	}
}

func TestForServiceTaskNotFound(t *testing.T) {
	c := NewClients(1, &taskClient{runningTasks: 0})
	c.SetRemote(2, &taskClient{})
	if _, _, err := c.ForServiceTask(context.Background(), 0, "mb-app-1"); !errors.Is(err, docker.ErrNotFound) {
		t.Fatalf("want docker.ErrNotFound, got %v", err)
	}
}

// Must report "cannot reach" rather than "nothing running", so the user installs the agent instead of chasing a crash.
func TestForServiceTaskRunningOnAnUnmanagedNode(t *testing.T) {
	// The manager sees no container of its own, but its swarm view says 1 task is up.
	c := NewClients(1, &taskClient{runningTasks: 1})

	_, _, err := c.ForServiceTask(context.Background(), 0, "mb-app-1")
	if !errors.Is(err, ErrTaskUnreachable) {
		t.Fatalf("want ErrTaskUnreachable, got %v", err)
	}
	if errors.Is(err, docker.ErrNotFound) {
		t.Error("a running task must not be reported as not-found — that is what made a " +
			"healthy 1/1 service look like it had no container")
	}
}

// Regression: the local engine manages another swarm, so asking it reported "nothing running" for a healthy service.
func TestForServiceTaskAsksTheServicesOwnSwarm(t *testing.T) {
	local := &taskClient{runningTasks: 0}
	remoteManager := &taskClient{runningTasks: 1}
	c := NewClients(1, local)
	c.SetSwarmManagers(func(_ context.Context, clusterID uint) (docker.Client, error) {
		if clusterID == 7 {
			return remoteManager, nil
		}
		return local, nil
	})

	if _, _, err := c.ForServiceTask(context.Background(), 7, "mb-app-1"); !errors.Is(err, ErrTaskUnreachable) {
		t.Fatalf("want ErrTaskUnreachable from the remote swarm's manager, got %v", err)
	}
}

// An unreachable manager leaves the question open; the local engine is not a stand-in for it.
func TestForServiceTaskManagerUnreachable(t *testing.T) {
	c := NewClients(1, &taskClient{runningTasks: 1})
	c.SetSwarmManagers(func(context.Context, uint) (docker.Client, error) { return nil, ErrNodeOffline })

	if _, _, err := c.ForServiceTask(context.Background(), 7, "mb-app-1"); !errors.Is(err, docker.ErrNotFound) {
		t.Fatalf("want docker.ErrNotFound, got %v", err)
	}
}
