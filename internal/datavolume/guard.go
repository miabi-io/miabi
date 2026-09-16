// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package datavolume refuses to start a workload whose data is gone.
//
// Docker creates a named volume silently when a container starts and it isn't there, so starting an app
// whose volume was deleted hands it an empty one, and starting a database on it initializes a new, empty
// cluster over the data it should have kept. Neither failure announces itself. The guard belongs on every
// path that starts a workload, because Miabi cannot put the data back: a restore is a person's decision.
package datavolume

import (
	"context"
	"errors"
	"fmt"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/drift"
	"github.com/miabi-io/miabi/internal/models"
)

// ErrLost refuses the start. Callers surface it as a conflict, not a server error: nothing is broken, the
// data is simply not there to start on.
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

// CheckApp refuses when any volume the app mounts has lost its data. Each volume is inspected on the node
// its record names, not on the app's: a shared volume exists on a node only once a task there mounted it,
// so asking the app's node would refuse starts that are perfectly fine.
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

// CheckVolume refuses when one named volume on a node has lost its data. label is what the refusal calls
// it, so an operator reads the name they know rather than a Docker one.
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

// watched reports whether a volume's absence from its node means its data is gone. A host-driver volume is
// a bind to an operator-managed path, and shared storage (nfs/cifs) lives on the backend rather than in the
// volume object, which each node creates on demand from the same mount options.
func watched(v *models.Volume) bool {
	return v.DockerName != "" && v.Driver != models.VolumeDriverHost &&
		v.Driver != models.VolumeDriverNFS && v.Driver != models.VolumeDriverCIFS
}
