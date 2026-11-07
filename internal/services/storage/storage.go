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

// Package storage manages persistent (Docker) volumes owned by workspaces.
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/hostmount"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// ErrInvalidDriver is returned for an unknown volume driver.
var ErrInvalidDriver = errors.New("invalid volume driver")

var ErrDriverDeviceRequired = errors.New("shared volume requires a 'device' driver option (the NFS export or CIFS share)")

var (
	ErrVolumeShrink     = errors.New("a volume's capacity can only be expanded")
	ErrVolumeBelowUsage = errors.New("declared size is below the volume's measured usage")
)

// ErrVolumeInUse is returned when deleting a volume still mounted by an app.
var ErrVolumeInUse = errors.New("volume is in use by one or more applications; detach it first")

// Wrapped with a message naming the owner so the user can delete it there instead.
var ErrVolumeOwned = errors.New("volume is owned by another resource")

var ErrHostPathRequired = errors.New("host-path volume requires a 'path' driver option (an absolute path under " + hostmount.CustomHostRoot + "/)")

var ErrHostMountNotPrivileged = errors.New("host-path volumes require a privileged workspace")

// Lets the service refuse to orphan an owned volume without depending on app/database/stack repos.
type OwnerExister func(kind string, id, workspaceID uint) bool

type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
	LocalID() uint
}

type NodeGuard interface {
	Placeable(serverID uint) error
}

type ServerInfo interface {
	Get(id uint) (*models.Server, error)
}

// Privileged flag is required, with the plan capability, to create host-path volumes.
type WorkspacePrivilege interface {
	FindByID(id uint) (*models.Workspace, error)
}

// Nil leaves every volume on the built-in class, i.e. the pre-storage-class behaviour.
type StorageClasses interface {
	Resolve(serverID uint, name string) (*models.StorageClass, error)
	EnsureDir(ctx context.Context, c *models.StorageClass, dockerName string) error
	RemoveDir(ctx context.Context, c *models.StorageClass, dockerName string) error
	DiskUsageAll(ctx context.Context, c *models.StorageClass) (map[string]int64, error)
	ListManaged() ([]models.StorageClass, error)
}

type Service struct {
	repo       *repositories.VolumeRepository
	apps       *repositories.ApplicationRepository
	clients    NodeDocker
	nodeGuard  NodeGuard
	serverInfo ServerInfo
	images     ImageResolver
	quota      *quota.Service
	ownerOf    OwnerExister
	workspaces WorkspacePrivilege
	classes    StorageClasses
}

func (s *Service) SetStorageClasses(c StorageClasses) { s.classes = c }

// Nil refuses host-path volumes.
func (s *Service) SetWorkspacePrivilege(w WorkspacePrivilege) { s.workspaces = w }

// SetQuota wires the plan/quota enforcer (nil-safe; nil skips checks).
func (s *Service) SetQuota(q *quota.Service) { s.quota = q }

func (s *Service) SetOwnerExister(fn OwnerExister) { s.ownerOf = fn }

func NewService(repo *repositories.VolumeRepository, apps *repositories.ApplicationRepository, clients NodeDocker) *Service {
	return &Service{repo: repo, apps: apps, clients: clients}
}

// Needs both the admin-granted privileged flag and the plan capability: host binds have the highest blast radius.
func (s *Service) requireHostMount(workspaceID uint) error {
	if s.quota != nil {
		if err := s.quota.Require(workspaceID, quota.CapPrivilegedHostMounts); err != nil {
			return err
		}
	}
	if s.workspaces == nil {
		return ErrHostMountNotPrivileged
	}
	ws, err := s.workspaces.FindByID(workspaceID)
	if err != nil {
		return err
	}
	if !ws.Privileged {
		return ErrHostMountNotPrivileged
	}
	return nil
}

// A plan's class list is enforced only for explicitly named classes so implicit creates don't fail on the node default.
func (s *Service) resolveClass(workspaceID, serverID uint, requested string) (*models.StorageClass, error) {
	if s.classes == nil {
		return nil, nil
	}
	var binding quota.StorageClassBinding
	if s.quota != nil {
		binding = s.quota.EffectiveStorageClasses(workspaceID)
	}
	name := strings.TrimSpace(requested)
	explicit := name != ""
	if !explicit {
		name = binding.Default
	}
	c, err := s.classes.Resolve(serverID, name)
	if err != nil {
		return nil, err
	}
	if binding.Allows(c.Name) {
		return c, nil
	}
	if explicit {
		return nil, s.quota.RequireStorageClass(workspaceID, c.Name)
	}
	return s.classes.Resolve(serverID, binding.Allowed[0])
}

// PlaceVolume gives self-managed Docker volumes (e.g. database data) the same class placement as workspace volumes.
func (s *Service) PlaceVolume(ctx context.Context, workspaceID, serverID uint, requested, dockerName string) (className, devicePath string, err error) {
	class, err := s.resolveClass(workspaceID, serverID, requested)
	if err != nil {
		return "", "", err
	}
	if class == nil {
		return models.DefaultStorageClassName, "", nil
	}
	// Docker accepts a bind-backed volume with a missing device and only fails at container start.
	if err := s.classes.EnsureDir(ctx, class, dockerName); err != nil {
		return "", "", err
	}
	return class.Name, class.VolumePath(dockerName), nil
}

// Best-effort: a leftover directory is a reclaimable orphan, while a failed delete leaves an unremovable resource.
func (s *Service) ReclaimVolumeDir(ctx context.Context, serverID uint, className, dockerName string) {
	if s.classes == nil || className == "" || className == models.DefaultStorageClassName {
		return
	}
	class, err := s.classes.Resolve(serverID, className)
	if err != nil {
		logger.Warn("reclaim: class lookup failed", "class", className, "volume", dockerName, "error", err)
		return
	}
	if err := s.classes.RemoveDir(ctx, class, dockerName); err != nil {
		logger.Warn("reclaim: remove directory failed", "class", className, "volume", dockerName, "error", err)
	}
}

// SetNodeGuard wires the placement guard consulted when creating a volume on a node.
func (s *Service) SetNodeGuard(g NodeGuard) { s.nodeGuard = g }

// SetServerInfo wires the resolver used to annotate volumes with their node's name.
func (s *Service) SetServerInfo(si ServerInfo) { s.serverInfo = si }

func (s *Service) annotateServer(v *models.Volume) {
	if v == nil || s.serverInfo == nil {
		return
	}
	if srv, err := s.serverInfo.Get(v.ServerID); err == nil && srv != nil {
		v.ServerName = srv.Label()
	}
}

// VolumeUsage is an application that mounts a volume, with its mount path.
type VolumeUsage struct {
	AppID          uint   `json:"app_id"`
	AppName        string `json:"app_name"`         // unique slug handle
	AppDisplayName string `json:"app_display_name"` // free-text label
	Path           string `json:"path"`
}

type VolumeDetail struct {
	models.Volume
	Driver string        `json:"driver,omitempty"`
	Exists bool          `json:"exists"` // whether the underlying Docker volume is present
	InUse  bool          `json:"in_use"`
	UsedBy []VolumeUsage `json:"used_by"`
}

func (s *Service) Detail(ctx context.Context, workspaceID, id uint) (*VolumeDetail, error) {
	v, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, err
	}
	s.annotateServer(v)
	d := &VolumeDetail{Volume: *v, UsedBy: []VolumeUsage{}}
	if dc, derr := s.clients.For(v.ServerID); derr == nil {
		if dv, err := dc.InspectVolume(ctx, v.DockerName); err == nil {
			d.Exists = true
			d.Driver = dv.Driver
			// For a class-backed volume the engine's mountpoint is the bind stub, not where the data is.
			if dv.Mountpoint != "" && v.StorageClassName == models.DefaultStorageClassName {
				d.Mountpoint = dv.Mountpoint
			}
		}
	}
	used, err := s.usedByApps(workspaceID, v.ID)
	if err != nil {
		return nil, err
	}
	d.UsedBy = used
	d.InUse = len(used) > 0
	return d, nil
}

func (s *Service) usedByApps(workspaceID, volumeID uint) ([]VolumeUsage, error) {
	apps, err := s.apps.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	used := []VolumeUsage{}
	for i := range apps {
		for _, m := range apps[i].Mounts {
			if m.VolumeID == volumeID {
				used = append(used, VolumeUsage{
					AppID: apps[i].ID, AppName: apps[i].Name, AppDisplayName: apps[i].DisplayName, Path: m.Path,
				})
			}
		}
	}
	return used, nil
}

// CreateInput is the full create surface for a managed volume.
type CreateInput struct {
	Name      string
	SizeBytes int64 // declared capacity (0 = unspecified/unlimited)
	// "" or "local" is node-local (rwo); nfs/cifs is shared (rwx); "host" binds an operator path.
	Driver string
	// Encrypted at rest since they may carry a CIFS password.
	DriverOpts map[string]string
	// Empty resolves through the workspace's plan and then the node's default class.
	StorageClass string
	Meta         models.Metadata
	Annotations  models.Metadata
}

// sizeBytes 0 means unspecified/unlimited.
func (s *Service) Create(ctx context.Context, workspaceID, serverID uint, name string, sizeBytes int64, meta, annotations models.Metadata) (*models.Volume, error) {
	return s.CreateOn(ctx, workspaceID, serverID, CreateInput{
		Name: name, SizeBytes: sizeBytes, Meta: meta, Annotations: annotations,
	})
}

// CreateWith creates a managed volume with an explicit driver and driver options.
func (s *Service) CreateWith(ctx context.Context, workspaceID, serverID uint, name string, sizeBytes int64, driver string, driverOpts map[string]string, meta, annotations models.Metadata) (*models.Volume, error) {
	return s.CreateOn(ctx, workspaceID, serverID, CreateInput{
		Name: name, SizeBytes: sizeBytes, Driver: driver, DriverOpts: driverOpts,
		Meta: meta, Annotations: annotations,
	})
}

// CreateOn creates a managed volume on a node, under the storage class the workspace resolves to.
func (s *Service) CreateOn(ctx context.Context, workspaceID, serverID uint, in CreateInput) (*models.Volume, error) {
	name, sizeBytes := in.Name, in.SizeBytes
	driverOpts, meta, annotations := in.DriverOpts, in.Meta, in.Annotations
	driver := strings.ToLower(strings.TrimSpace(in.Driver))
	if driver == "" {
		driver = models.VolumeDriverLocal
	}
	accessMode := models.AccessRWO
	hostPath := ""
	dockerSpec := docker.VolumeSpec{Labels: map[string]string{docker.LabelWorkspace: fmt.Sprint(workspaceID)}, SizeBytes: sizeBytes}
	switch driver {
	case models.VolumeDriverLocal:
		// node-local default; no driver options.
		driverOpts = nil
	case models.VolumeDriverNFS, models.VolumeDriverCIFS:
		// Shared storage (rwx) is a plan capability — node-local volumes stay free.
		if err := s.quota.Require(workspaceID, quota.CapSharedStorage); err != nil {
			return nil, err
		}
		accessMode = models.AccessRWX

		opts, verr := validateSharedDriverOpts(driver, driverOpts)
		if verr != nil {
			return nil, verr
		}
		driverOpts = opts
		dockerSpec.DriverOpts = opts // Driver stays "" (local backing) by design
	case models.VolumeDriverHost:
		// Host paths are mounted identically on every node, so treated as rwx; privileged workspaces only.
		if err := s.requireHostMount(workspaceID); err != nil {
			return nil, err
		}
		if strings.TrimSpace(driverOpts["path"]) == "" {
			return nil, ErrHostPathRequired
		}
		clean, verr := hostmount.ValidateCustomHostPath(driverOpts["path"])
		if verr != nil {
			return nil, verr
		}
		hostPath = clean
		accessMode = models.AccessRWX
		driverOpts = nil // no Docker volume opts — this is a bind, not a driver volume
	default:
		return nil, ErrInvalidDriver
	}

	if sizeBytes < 0 {
		sizeBytes = 0
	}
	if s.quota.Enabled() {
		n, _ := s.repo.CountByWorkspace(workspaceID)
		if err := s.quota.CheckCreate(workspaceID, quota.ResourceVolumes, int(n)); err != nil {
			return nil, err
		}
		if err := s.quota.CheckStorageAdd(workspaceID, sizeBytes); err != nil {
			return nil, err
		}
	}
	if serverID == 0 {
		serverID = s.clients.LocalID()
	}
	if s.nodeGuard != nil {
		if err := s.nodeGuard.Placeable(serverID); err != nil {
			return nil, err
		}
	}
	dc, err := s.clients.For(serverID)
	if err != nil {
		return nil, err
	}
	// nfs/cifs name their own backend and host-path binds an operator-owned directory, so only local gets a class.
	var class *models.StorageClass
	if driver == models.VolumeDriverLocal {
		if class, err = s.resolveClass(workspaceID, serverID, in.StorageClass); err != nil {
			return nil, err
		}
	}
	volName, err := slug.Unique(name, "vol", func(c string) (bool, error) {
		return s.repo.ExistsByName(workspaceID, c)
	})
	if err != nil {
		return nil, err
	}
	dockerName := fmt.Sprintf("mb-vol-%d-%s", workspaceID, volName)
	dockerSpec.Name = dockerName
	className := models.DefaultStorageClassName
	if class != nil {
		className = class.Name
		// Docker accepts a bind-backed volume with a missing device and only fails at container start.
		if err := s.classes.EnsureDir(ctx, class, dockerName); err != nil {
			return nil, err
		}
		dockerSpec.DevicePath = class.VolumePath(dockerName)
	}
	// May carry a CIFS password, so encrypted and never returned; volumes are immutable, so create-only.
	optsEnc := ""
	if len(driverOpts) > 0 {
		if raw, merr := json.Marshal(driverOpts); merr == nil {
			if enc, eerr := crypto.EncryptWS(workspaceID, string(raw)); eerr == nil {
				optsEnc = enc
			}
		}
	}
	v := &models.Volume{
		WorkspaceID: workspaceID, Name: volName, DisplayName: name, ServerID: serverID,
		DockerName: dockerName, Mountpoint: hostPath, SizeBytes: sizeBytes,
		Driver: driver, AccessMode: accessMode, DriverOptsEnc: optsEnc, HostPath: hostPath,
		StorageClassName: className,
		// Richer callers (marketplace/stack/apply) pass their own via meta and win over these defaults.
		Metadata:    models.DefaultManagedBy(meta, models.ManagedByUser),
		Annotations: annotations,
	}
	// The row goes first so the Docker volume's create-time labels can carry its id for orphan housekeeping.
	if err := s.repo.Create(v); err != nil {
		return nil, err
	}
	// A host-path volume is a bind, not a Docker volume, so there is nothing to create.
	if driver != models.VolumeDriverHost {
		dockerSpec.Labels[docker.LabelVolume] = fmt.Sprint(v.ID)
		dv, cerr := dc.CreateVolumeWith(ctx, dockerSpec)
		if cerr != nil {
			_ = s.repo.Delete(v.ID)
			if class != nil {
				_ = s.classes.RemoveDir(ctx, class, dockerName)
			}
			return nil, cerr
		}
		// For a class-backed volume the engine reports its stub; the data lives behind the bind.
		if p := dockerSpec.DevicePath; p != "" {
			v.Mountpoint = p
		} else if dv.Mountpoint != "" {
			v.Mountpoint = dv.Mountpoint
		}
		// Lets a volume deleted and recreated by hand later read as replaced instead of intact.
		v.EngineCreatedAt = dv.CreatedAt
		if err := s.repo.Update(v); err != nil {
			return nil, err
		}
	}
	return v, nil
}

func (s *Service) List(workspaceID uint) ([]models.Volume, error) {
	vols, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	for i := range vols {
		s.annotateServer(&vols[i])
	}
	return vols, nil
}

func (s *Service) Get(workspaceID, id uint) (*models.Volume, error) {
	v, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, err
	}
	s.annotateServer(v)
	return v, nil
}

// Built-in keys are authoritative, so this overrides any prior owner.
func (s *Service) SetOwner(workspaceID, id uint, kind string, ownerID uint, name string) error {
	v, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return err
	}
	v.Metadata = models.SetOwner(v.Metadata, kind, ownerID, name)
	return s.repo.Update(v)
}

// A user owner, a missing owner, or one that no longer exists does not block deletion.
func (s *Service) guardOwned(meta models.Metadata, workspaceID uint) error {
	ref, ok := models.Owner(meta)
	if !ok || ref.Kind == models.OwnerUser || ref.ID == 0 || s.ownerOf == nil {
		return nil
	}
	if !s.ownerOf(ref.Kind, ref.ID, workspaceID) {
		return nil // owner already gone — the reference is stale, allow delete
	}
	name := ref.Name
	if name == "" {
		name = fmt.Sprintf("#%d", ref.ID)
	}
	return fmt.Errorf("%w: it backs %s %q — delete that instead", ErrVolumeOwned, ref.Kind, name)
}

func (s *Service) Delete(ctx context.Context, v *models.Volume) error {
	used, err := s.usedByApps(v.WorkspaceID, v.ID)
	if err != nil {
		return err
	}
	if len(used) > 0 {
		return ErrVolumeInUse
	}
	if err := s.guardOwned(v.Metadata, v.WorkspaceID); err != nil {
		return err
	}
	// Miabi must never delete operator host data, so a host-path volume only drops its record.
	if v.Driver != models.VolumeDriverHost && !s.nodeGone(v) {
		dc, err := s.clients.For(v.ServerID)
		if err != nil {
			return err
		}
		if err := dc.RemoveVolume(ctx, v.DockerName, false); err != nil {
			return fmt.Errorf("remove docker volume (in use?): %w", err)
		}
		// docker volume rm leaves the data behind the bind, so the class reclaim policy must remove it here.
		s.reclaim(ctx, v)
	}
	return s.repo.Delete(v.ID)
}

// Resize changes only declared capacity (quota and UI); nothing on disk moves, and it can only grow.
func (s *Service) Resize(workspaceID, id uint, sizeBytes int64) error {
	v, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return err
	}
	if sizeBytes < 0 {
		sizeBytes = 0
	}
	if v.SizeBytes > 0 && sizeBytes < v.SizeBytes {
		return fmt.Errorf("%w: %s has %d bytes, %d requested", ErrVolumeShrink, v.Name, v.SizeBytes, sizeBytes)
	}
	if sizeBytes > 0 && sizeBytes < v.UsedBytes {
		return fmt.Errorf("%w: %s already holds %d bytes", ErrVolumeBelowUsage, v.Name, v.UsedBytes)
	}
	if delta := sizeBytes - v.SizeBytes; delta > 0 && s.quota.Enabled() {
		if err := s.quota.CheckStorageAdd(workspaceID, delta); err != nil {
			return err
		}
	}
	v.SizeBytes = sizeBytes
	return s.repo.Update(v)
}

// nodeGone reports whether the node a volume lived on has been deleted.
func (s *Service) nodeGone(v *models.Volume) bool {
	if v == nil || s.serverInfo == nil {
		return false // unwired: assume the node is fine rather than drop a row on a guess
	}
	if s.clients != nil && v.ServerID == s.clients.LocalID() {
		return false // the control-plane node is never deleted
	}
	srv, err := s.serverInfo.Get(v.ServerID)
	return err != nil || srv == nil
}

// Best-effort: a leftover directory is a reclaimable orphan, while a failed delete leaves an unremovable volume.
func (s *Service) reclaim(ctx context.Context, v *models.Volume) {
	if s.classes == nil || v.StorageClassName == "" || v.StorageClassName == models.DefaultStorageClassName {
		return
	}
	class, err := s.classes.Resolve(v.ServerID, v.StorageClassName)
	if err != nil {
		logger.Warn("volume reclaim: class lookup failed", "volume", v.ID, "class", v.StorageClassName, "error", err)
		return
	}
	if err := s.classes.RemoveDir(ctx, class, v.DockerName); err != nil {
		logger.Warn("volume reclaim: remove directory failed", "volume", v.ID, "class", class.Name, "error", err)
	}
}

// IDByUID resolves a volume's portable uid to its numeric id.
func (s *Service) IDByUID(uid string) (uint, error) { return s.repo.IDByUID(uid) }
