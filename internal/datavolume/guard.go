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

// Package datavolume refuses to start a workload whose volume is gone, since Docker would silently create an empty one.
package datavolume

import (
	"context"
	"errors"
	"fmt"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/drift"
	"github.com/miabi-io/miabi/internal/models"
)

// ErrLost is a conflict, not a server error: nothing is broken, the data is simply not there.
var ErrLost = errors.New("a volume holding this workload's data is gone; restore it from a backup first")

// Volumes resolves the volume rows a workload mounts. Satisfied by *repositories.VolumeRepository.
type Volumes interface {
	FindInWorkspace(workspaceID, id uint) (*models.Volume, error)
	FindByDockerName(name string) (*models.Volume, error)
}

// Clients resolves a node's engine. Satisfied by *nodes.Clients.
type Clients interface {
	For(serverID uint) (docker.Client, error)
}

// CheckApp inspects each volume on its recorded node: a shared volume exists on a node only once a task mounted it.
func CheckApp(ctx context.Context, clients Clients, vols Volumes, app *models.Application) error {
	if clients == nil || vols == nil || app == nil {
		return nil
	}
	for _, m := range app.Mounts {
		// A host bind, a preset host path and a projected config are not Docker volumes.
		if m.HostPreset != "" || m.HostPath != "" || m.ConfigID != 0 {
			continue
		}
		v := resolve(vols, app.WorkspaceID, m)
		if v == nil || !watched(v) {
			continue
		}
		if err := CheckVolume(ctx, clients, v.ServerID, v.DockerName, v.EngineCreatedAt, v.Name); err != nil {
			return err
		}
	}
	return nil
}

func CheckVolume(ctx context.Context, clients Clients, serverID uint, dockerName, recordedCreatedAt, label string) error {
	if clients == nil || dockerName == "" {
		return nil
	}
	dc, err := clients.For(serverID)
	if err != nil {
		return nil // the node is unreachable: the start fails on its own, and this is not evidence of loss
	}
	class, err := drift.VolumeState(ctx, dc, dockerName, recordedCreatedAt)
	if err != nil || class == "" {
		return nil
	}
	if label == "" {
		label = dockerName
	}
	return fmt.Errorf("%w (volume %s is %s)", ErrLost, label, class)
}

func resolve(vols Volumes, workspaceID uint, m models.AppMount) *models.Volume {
	if m.VolumeID != 0 {
		if v, err := vols.FindInWorkspace(workspaceID, m.VolumeID); err == nil {
			return v
		}
	}
	if m.DockerName != "" {
		if v, err := vols.FindByDockerName(m.DockerName); err == nil {
			return v
		}
	}
	return nil
}

// Host-driver binds and shared nfs/cifs storage live outside the volume object, so their absence is not loss.
func watched(v *models.Volume) bool {
	return v.DockerName != "" && v.Driver != models.VolumeDriverHost &&
		v.Driver != models.VolumeDriverNFS && v.Driver != models.VolumeDriverCIFS
}
