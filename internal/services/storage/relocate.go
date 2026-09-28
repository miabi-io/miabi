// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

// ErrHostVolumeImmovable refuses to move a host-path volume: its data is a directory on the node that
// Miabi does not own.
var ErrHostVolumeImmovable = errors.New("a host-path volume cannot be moved: its data is an operator-managed directory on the node")

// MoveTarget is the copy of a volume prepared on another node.
type MoveTarget struct {
	StorageClass string `json:"storage_class"`
	Mountpoint   string `json:"mountpoint"`
	CreatedAt    string `json:"created_at"`
}

func (s *Service) PrepareMove(ctx context.Context, v *models.Volume, serverID uint) (MoveTarget, error) {
	if v.Driver == models.VolumeDriverHost {
		return MoveTarget{}, ErrHostVolumeImmovable
	}
	dc, err := s.clients.For(serverID)
	if err != nil {
		return MoveTarget{}, err
	}
	spec := docker.VolumeSpec{
		Name: v.DockerName,
		Labels: map[string]string{
			docker.LabelWorkspace: fmt.Sprint(v.WorkspaceID),
			docker.LabelVolume:    fmt.Sprint(v.ID),
		},
		SizeBytes: v.SizeBytes,
	}
	target := MoveTarget{StorageClass: models.DefaultStorageClassName}
	switch v.Driver {
	case models.VolumeDriverNFS, models.VolumeDriverCIFS:
		opts, err := s.driverOpts(v)
		if err != nil {
			return MoveTarget{}, err
		}
		spec.DriverOpts = opts
	default:
		if class := s.classOn(serverID, v.StorageClassName); class != nil {
			if err := s.classes.EnsureDir(ctx, class, v.DockerName); err != nil {
				return MoveTarget{}, err
			}
			spec.DevicePath = class.VolumePath(v.DockerName)
			target.StorageClass = class.Name
		}
	}
	dv, err := dc.CreateVolumeWith(ctx, spec)
	if err != nil {
		return MoveTarget{}, err
	}
	target.CreatedAt = dv.CreatedAt
	target.Mountpoint = dv.Mountpoint
	if spec.DevicePath != "" {
		target.Mountpoint = spec.DevicePath
	}
	return target, nil
}

// DropCopy removes a volume's copy from a node that no longer holds it: the source after a move is
// finalized, or the target after one is rolled back. Best-effort on the class directory, like Delete.
func (s *Service) DropCopy(ctx context.Context, serverID uint, dockerName, className string) error {
	dc, err := s.clients.For(serverID)
	if err != nil {
		return err
	}
	if err := dc.RemoveVolume(ctx, dockerName, true); err != nil {
		return err
	}
	s.ReclaimVolumeDir(ctx, serverID, className, dockerName)
	return nil
}

// VolumeConsumers lists the applications that mount a volume.
func (s *Service) VolumeConsumers(workspaceID, volumeID uint) ([]VolumeUsage, error) {
	return s.usedByApps(workspaceID, volumeID)
}

// classOn resolves a storage class by name on a node, or nil for the built-in class or a node that does
// not register it.
func (s *Service) classOn(serverID uint, name string) *models.StorageClass {
	if s.classes == nil || name == "" || name == models.DefaultStorageClassName {
		return nil
	}
	c, err := s.classes.Resolve(serverID, name)
	if err != nil {
		return nil
	}
	return c
}

// ClassAvailable reports whether a node registers a storage class, so a plan can say where a volume lands.
func (s *Service) ClassAvailable(serverID uint, name string) bool {
	return name == "" || name == models.DefaultStorageClassName || s.classOn(serverID, name) != nil
}

func (s *Service) driverOpts(v *models.Volume) (map[string]string, error) {
	if v.DriverOptsEnc == "" {
		return nil, nil
	}
	raw, err := crypto.Decrypt(v.DriverOptsEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt volume options: %w", err)
	}
	var opts map[string]string
	if err := json.Unmarshal([]byte(raw), &opts); err != nil {
		return nil, fmt.Errorf("read volume options: %w", err)
	}
	return opts, nil
}
