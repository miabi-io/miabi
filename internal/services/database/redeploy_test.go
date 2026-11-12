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

package database

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/datavolume"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

// errQueued stops the call right after enqueueing so the test needs no repository.
var errQueued = errors.New("queued")

type recordingEnqueuer struct {
	Enqueuer
	redeployed []uint
}

func (r *recordingEnqueuer) EnqueueRedeployDB(instanceID, _ uint) error {
	r.redeployed = append(r.redeployed, instanceID)
	return errQueued
}

type lostContainerEngine struct{ guardEngine }

func (lostContainerEngine) StartContainer(context.Context, string) error { return docker.ErrNotFound }
func (lostContainerEngine) RestartContainer(context.Context, string, int) error {
	return docker.ErrNotFound
}

func intactVolumes() map[string]docker.Volume {
	return map[string]docker.Volume{"mb-db-x-5-data": {Name: "mb-db-x-5-data", CreatedAt: "2026-09-01T10:00:00Z"}}
}

func TestStartAndRestartRedeployALostContainer(t *testing.T) {
	q := &recordingEnqueuer{}
	s := &Service{clients: guardClients{1: lostContainerEngine{guardEngine{volumes: intactVolumes()}}}, enqueuer: q}
	pg := func() *models.DatabaseInstance {
		inst := guardedInstance()
		inst.Engine = models.DBEnginePostgres
		return inst
	}

	if err := s.Start(context.Background(), pg()); !errors.Is(err, errQueued) {
		t.Fatalf("Start err = %v; want the redeploy to be queued", err)
	}
	if err := s.Restart(context.Background(), pg()); !errors.Is(err, errQueued) {
		t.Fatalf("Restart err = %v; want the redeploy to be queued", err)
	}
	unrecorded := pg()
	unrecorded.ContainerID = ""
	if err := s.Start(context.Background(), unrecorded); !errors.Is(err, errQueued) {
		t.Fatalf("Start without a container err = %v; want the redeploy to be queued", err)
	}
	if len(q.redeployed) != 3 {
		t.Fatalf("redeploys queued = %v; want 3", q.redeployed)
	}
}

func TestRedeployRefusesUnsafeStates(t *testing.T) {
	q := &recordingEnqueuer{}
	s := &Service{clients: guardClients{1: guardEngine{volumes: map[string]docker.Volume{}}}, enqueuer: q}

	inst := guardedInstance()
	inst.Engine = models.DBEnginePostgres
	if err := s.Redeploy(context.Background(), inst); !errors.Is(err, datavolume.ErrLost) {
		t.Fatalf("lost volume: err = %v; want datavolume.ErrLost", err)
	}
	inst.Status = models.DBStatusUpgrading
	if err := s.Redeploy(context.Background(), inst); !errors.Is(err, ErrUpgradeInProgress) {
		t.Fatalf("upgrading: err = %v; want ErrUpgradeInProgress", err)
	}

	provisioning := guardedInstance()
	provisioning.ContainerID, provisioning.Status = "", models.DBStatusProvisioning
	intact := &Service{clients: guardClients{1: guardEngine{volumes: intactVolumes()}}, enqueuer: q}
	if err := intact.Start(context.Background(), provisioning); !errors.Is(err, ErrInstanceBusy) {
		t.Fatalf("Start while provisioning: err = %v; want ErrInstanceBusy", err)
	}

	gone := &Service{clients: fakeClients{local: 1}, serverInfo: fakeServers{exists: map[uint]bool{1: true}}, enqueuer: q}
	orphan := guardedInstance()
	orphan.Engine, orphan.ServerID = models.DBEnginePostgres, 9
	if err := gone.Redeploy(context.Background(), orphan); !errors.Is(err, ErrNodeGone) {
		t.Fatalf("deleted node: err = %v; want ErrNodeGone", err)
	}
	if len(q.redeployed) != 0 {
		t.Fatalf("redeploys queued = %v; want none", q.redeployed)
	}
}
