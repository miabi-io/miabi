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

package application

import (
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestPinNode(t *testing.T) {
	released := uint(9)
	pgdata := &models.Volume{ID: 1, Name: "pgdata", ServerID: 3, Driver: models.VolumeDriverLocal, AccessMode: models.AccessRWO}
	shared := &models.Volume{ID: 2, Name: "media", ServerID: 3, Driver: models.VolumeDriverLocal, AccessMode: models.AccessRWX}
	s := &Service{volumes: fakeVolumes{byID: map[uint]*models.Volume{1: pgdata, 2: shared}}}

	svc := &models.Application{Name: "api", RuntimeKind: models.RuntimeService, ServerID: 1}
	if err := s.PinNode(svc, 4); err != nil || svc.PinnedServerID != 4 || svc.ServerID != 1 {
		t.Fatalf("service pin = %v, pinned %d, server %d; want pinned 4 and the manager kept", err, svc.PinnedServerID, svc.ServerID)
	}
	if err := s.PinNode(svc, 0); err != nil || svc.PinnedServerID != 0 {
		t.Fatalf("unpin = %v, pinned %d", err, svc.PinnedServerID)
	}

	fresh := &models.Application{Name: "web", RuntimeKind: models.RuntimeContainer, ServerID: 3}
	if err := s.PinNode(fresh, 4); err != nil || fresh.ServerID != 4 || fresh.PinnedServerID != 4 {
		t.Fatalf("undeployed container pin = %v, server %d; want it moved to 4", err, fresh.ServerID)
	}

	deployed := &models.Application{Name: "web", RuntimeKind: models.RuntimeContainer, ServerID: 3, CurrentReleaseID: &released}
	if err := s.PinNode(deployed, 4); !errors.Is(err, ErrNodeMoveDeployed) {
		t.Errorf("deployed container move err = %v, want ErrNodeMoveDeployed", err)
	}
	if err := s.PinNode(deployed, 3); err != nil || deployed.PinnedServerID != 3 {
		t.Errorf("pinning a deployed container to its own node = %v", err)
	}

	withVolume := &models.Application{Name: "db", RuntimeKind: models.RuntimeService, ServerID: 1,
		Mounts: []models.AppMount{{VolumeID: 1, Path: "/data"}, {VolumeID: 2, Path: "/media"}}}
	err := s.PinNode(withVolume, 4)
	if !errors.Is(err, ErrNodeVolumeConflict) || !strings.Contains(err.Error(), `volume "pgdata" lives on`) {
		t.Errorf("pin away from a node-local volume err = %v, want NODE_VOLUME_CONFLICT naming it", err)
	}
	if err := s.PinNode(withVolume, 3); err != nil {
		t.Errorf("pin to the volume's node = %v (a shared volume must not count)", err)
	}

	constrained := &models.Application{RuntimeKind: models.RuntimeService, PlacementConstraints: []string{"node.hostname == n2"}}
	if err := s.PinNode(constrained, 4); !errors.Is(err, ErrNodeConstraintConflict) {
		t.Errorf("pin beside a node constraint err = %v, want ErrNodeConstraintConflict", err)
	}
	labelled := &models.Application{RuntimeKind: models.RuntimeService, PlacementConstraints: []string{"node.labels.disk==ssd"}}
	if err := s.PinNode(labelled, 4); err != nil {
		t.Errorf("a label constraint does not choose the node: %v", err)
	}
}
