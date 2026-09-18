// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// ErrDriverDeviceRequired is returned when a shared-storage volume (nfs/cifs) is
// created without a backing device (the export/share path).
var ErrDriverDeviceRequired = errors.New("shared volume requires a 'device' driver option (the NFS export or CIFS share)")

// ErrVolumeShrink is returned when a declared capacity is lowered below what the volume already
// holds on disk.
var ErrVolumeShrink = errors.New("declared size is below the volume's measured usage")

// ErrVolumeInUse is returned when deleting a volume still mounted by an app.
var ErrVolumeInUse = errors.New("volume is in use by one or more applications; detach it first")

// ErrVolumeOwned is returned when deleting a volume whose owning resource (the
// app/database/stack it backs) still exists. Wrapped with a message naming the
// owner so callers can guide the user to delete it there instead.
var ErrVolumeOwned = errors.New("volume is owned by another resource")

// ErrHostPathRequired is returned when a "host" driver volume is created without
// a host path.
var ErrHostPathRequired = errors.New("host-path volume requires a 'path' driver option (an absolute path under " + hostmount.CustomHostRoot + "/)")

// ErrHostMountNotPrivileged is returned when a non-privileged workspace tries to
// create a host-path volume.
var ErrHostMountNotPrivileged = errors.New("host-path volumes require a privileged workspace")

// OwnerExister reports whether an owning resource of the given kind and id still exists in the workspace.
// Wired by the composition root so the service can refuse to orphan a resource whose owner is still around,
// without depending on the app, database or stack repositories directly.
type OwnerExister func(kind string, id, workspaceID uint) bool

// NodeDocker resolves the Docker client for a node id (0 = local). Implemented
// by nodes.Clients; an interface here keeps services decoupled from the agent
// machinery.
type NodeDocker interface {
	For(serverID uint) (docker.Client, error)
	LocalID() uint
}

// NodeGuard validates that a node can accept a new placement (exists, not
// cordoned). Implemented by the node service; injected after construction.
type NodeGuard interface {
	Placeable(serverID uint) error
}

// ServerInfo resolves a node's display metadata by id (implemented by the node
// service; optional).
type ServerInfo interface {
	Get(id uint) (*models.Server, error)
}

// WorkspacePrivilege reports whether a workspace holds the platform-admin-granted
// privileged flag, required (with the plan capability) to create host-path
// volumes. Implemented by the workspace repository; injected after construction.
type WorkspacePrivilege interface {
	FindByID(id uint) (*models.Workspace, error)
}

// StorageClasses resolves the admin-registered directory a volume is created under and owns the
// node-side filesystem actions that go with it. Implemented by storageclass.Service; nil leaves
// every volume on the built-in class, i.e. exactly the pre-storage-class behaviour.
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

// SetStorageClasses wires the storage-class resolver (nil-safe; nil keeps every volume on the
// engine's own data root).
func (s *Service) SetStorageClasses(c StorageClasses) { s.classes = c }

// SetWorkspacePrivilege wires the privileged-workspace check gating host-path
// volume creation (nil-safe; nil refuses host-path volumes).
func (s *Service) SetWorkspacePrivilege(w WorkspacePrivilege) { s.workspaces = w }

// SetQuota wires the plan/quota enforcer (nil-safe; nil skips checks).
func (s *Service) SetQuota(q *quota.Service) { s.quota = q }

// SetOwnerExister wires the owner-existence check used by Delete to guard a
// volume that still backs an app/database/stack (nil-safe; nil skips the guard).
func (s *Service) SetOwnerExister(fn OwnerExister) { s.ownerOf = fn }

func NewService(repo *repositories.VolumeRepository, apps *repositories.ApplicationRepository, clients NodeDocker) *Service {
	return &Service{repo: repo, apps: apps, clients: clients}
}

// requireHostMount gates host-path volume creation: the workspace must both hold the
// platform-admin-granted privileged flag AND the plan's privileged-host-mount capability (the same gate as
// app-level host binds), because a host bind is the highest-blast-radius storage action.
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

// resolveClass picks the storage class a volume is created under: an explicitly named class, else
// the workspace plan's default, else the node's default, else the built-in one. A plan that lists
// classes refuses one outside the list — but only when the tenant asked for it by name, because an
// implicit create must not fail just because the node's default is not on that plan.
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

// VolumeDetail enriches a stored volume with live Docker info and the apps that
// mount it.
type VolumeDetail struct {
	models.Volume
	Driver string        `json:"driver,omitempty"`
	Exists bool          `json:"exists"` // whether the underlying Docker volume is present
	InUse  bool          `json:"in_use"`
	UsedBy []VolumeUsage `json:"used_by"`
}

// Detail returns a volume with its live Docker driver/mountpoint and the list of
// applications that currently mount it.
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
			// Keep the stored path for a class-backed volume: the engine's is the bind stub, not
			// where the data is (see CreateOn).
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
	// Driver is "" or "local" for a node-local volume (rwo), "nfs"/"cifs" for shared storage (rwx)
	// a replicated service can mount across nodes, or "host" for a bind to an operator path.
	Driver string
	// DriverOpts are the backend mount options, encrypted at rest since they may carry a CIFS
	// password.
	DriverOpts map[string]string
	// StorageClass names the admin-registered directory the volume is created under. Empty resolves
	// through the workspace's plan and then the node's default class.
	StorageClass string
	Meta         models.Metadata
	Annotations  models.Metadata
}

// Create provisions a managed Docker volume on the given node (serverID 0 =
// local). sizeBytes is the declared capacity (0 = unspecified/unlimited).
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
		// A bind to an operator-managed host path (under /mnt/*), meant for storage mounted at the same path on
		// every node — so it is treated as rwx (a replicated service can share it). Privileged workspaces only:
		// this is the highest-blast-radius storage action.
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
	// Only a node-local volume has a storage class: nfs/cifs name their own backend, and a
	// host-path volume is a bind to a directory the operator already owns.
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
		// The directory must exist before a container mounts the volume: Docker accepts a bind-
		// backed volume whose device is missing and only fails at container start.
		if err := s.classes.EnsureDir(ctx, class, dockerName); err != nil {
			return nil, err
		}
		dockerSpec.DevicePath = class.VolumePath(dockerName)
	}
	// Encrypt the backing driver options at rest (they may carry a CIFS password)
	// and never return them. The volume is immutable, so options are create-only.
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
		// Default provenance + owner; richer callers (marketplace/stack/apply) pass
		// their own via meta and win over these defaults.
		Metadata:    models.DefaultManagedBy(meta, models.ManagedByUser),
		Annotations: annotations,
	}
	// The row goes first so the Docker volume can carry its id: labels are fixed at
	// create, and housekeeping needs that back-reference to find an orphaned volume.
	if err := s.repo.Create(v); err != nil {
		return nil, err
	}
	// A host-path volume is a bind, not a Docker volume — nothing to create; its
	// "mountpoint" is the host path the operator manages, present on every node.
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
		// For a class-backed volume the engine reports its own stub (/var/lib/docker/volumes/<name>/
		// _data) and the data lives behind the bind, so recording the engine's answer would point an
		// operator at a directory that is empty unless a container has the volume mounted.
		if p := dockerSpec.DevicePath; p != "" {
			v.Mountpoint = p
		} else if dv.Mountpoint != "" {
			v.Mountpoint = dv.Mountpoint
		}
		// The engine's creation timestamp is recorded so a volume deleted and recreated by hand later reads
		// as replaced instead of intact.
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

// SetOwner records the owning resource (app, database, stack or user) on an existing volume's metadata. Used
// to back-link template volumes to the app or stack they back once those have been created. Built-in keys are
// authoritative, so this overrides any prior owner.
func (s *Service) SetOwner(workspaceID, id uint, kind string, ownerID uint, name string) error {
	v, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return err
	}
	v.Metadata = models.SetOwner(v.Metadata, kind, ownerID, name)
	return s.repo.Update(v)
}

// guardOwned refuses deletion when the resource records an owner (app/database/ stack) that still exists,
// so users delete the owner instead of orphaning it. A user owner, a missing/zero owner, or an owner that
// no longer exists do not block. No-op when no owner-existence checker is wired.
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

// Delete removes the volume record and the underlying Docker volume. Refuses
// when any application still mounts it (guards against deleting live data) or
// when it still backs an owning app/database/stack.
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
	// A host-path volume is a bind to an operator-managed directory — there is no
	// Docker volume to remove, and Miabi must never delete the host data. Just drop
	// the record.
	if v.Driver != models.VolumeDriverHost {
		dc, err := s.clients.For(v.ServerID)
		if err != nil {
			return err
		}
		if err := dc.RemoveVolume(ctx, v.DockerName, false); err != nil {
			return fmt.Errorf("remove docker volume (in use?): %w", err)
		}
		// `docker volume rm` drops the volume's mountpoint stub and leaves the data behind the
		// bind untouched, so a class-backed volume orphans every byte it held unless the class's
		// reclaim policy removes the directory here.
		s.reclaim(ctx, v)
	}
	return s.repo.Delete(v.ID)
}

// Resize changes a volume's DECLARED capacity. That number drives quota accounting and the UI, not
// the filesystem: hard enforcement depends on the node's storage backend, so nothing on disk moves
// and the engine's create-time size label keeps its original value. Shrinking below what the volume
// already holds is refused — the number would be a lie the moment it was saved.
func (s *Service) Resize(workspaceID, id uint, sizeBytes int64) error {
	v, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return err
	}
	if sizeBytes < 0 {
		sizeBytes = 0
	}
	if sizeBytes > 0 && sizeBytes < v.UsedBytes {
		return fmt.Errorf("%w: %s already holds %d bytes", ErrVolumeShrink, v.Name, v.UsedBytes)
	}
	if delta := sizeBytes - v.SizeBytes; delta > 0 && s.quota.Enabled() {
		if err := s.quota.CheckStorageAdd(workspaceID, delta); err != nil {
			return err
		}
	}
	v.SizeBytes = sizeBytes
	return s.repo.Update(v)
}

// reclaim removes a class-backed volume's directory when the class says to. Best-effort and never
// fatal: the volume row and the Docker volume are already gone, and a leftover directory is a
// reclaimable orphan, while a failed delete would leave the user with a volume they cannot remove.
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
