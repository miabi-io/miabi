// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package housekeeping reconciles a node's real Docker state against what Miabi intends, serving three
// faces of drift: a disk report, a safe categorized prune with a dry-run preview, and a reconcile that
// classifies orphan/missing/untracked by joining live Docker against the DB by the miabi.* labels.
package housekeeping

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/drift"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/node"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// Clients resolves a node's Docker client (local or remote agent).
type Clients interface {
	For(serverID uint) (docker.Client, error)
}

// appLister yields the apps placed on a node, for missing-app detection. Backed
// by the application repository in production; a fake in tests.
type appLister interface {
	ListByServer(serverID uint) ([]models.Application, error)
}

// SwarmManagers resolves the client that drives a cluster's swarm. Implemented by the cluster service.
type SwarmManagers interface {
	Manager(ctx context.Context, clusterID uint) (docker.Client, error)
}

// recordExister reports whether the owning DB record for a managed resource (by
// kind + id) still exists. A soft-deleted record reads as absent — the orphan
// condition. Backed by the repos in production; a fake in tests.
type recordExister func(kind string, id uint) (bool, error)

// Service computes housekeeping reports and applies reclaim/reconcile actions.
type Service struct {
	clients Clients
	apps    appLister
	exists  recordExister
	// configs resolves a workspace config by name, so an orphaned swarm config
	// object can be told apart from a live one. nil never classifies an orphan.
	configs configLookup
	// volumeNamed reports whether a volume row claims a Docker volume name, for
	// volumes created before they were labelled. nil never classifies an orphan.
	volumeNamed func(dockerName string) (bool, error)
	// swarms answers for service apps, whose tasks may run on any node of their
	// cluster. nil never reports a service app missing.
	swarms SwarmManagers
}

// configLookup resolves a config by name within a workspace.
type configLookup interface {
	GetByName(workspaceID uint, name string) (*models.Config, error)
}

// SetConfigs wires the config lookup used to classify swarm config objects.
func (s *Service) SetConfigs(c configLookup) { s.configs = c }

// SetSwarmManagers wires the cluster manager lookup used to check service apps.
func (s *Service) SetSwarmManagers(m SwarmManagers) { s.swarms = m }

// NewService wires the housekeeping service against the node client registry and
// the repos it joins live Docker state against. The repos are composed into a
// single existence check so the drift analyzer stays decoupled from GORM.
func NewService(
	clients Clients,
	apps *repositories.ApplicationRepository,
	dbs *repositories.DatabaseRepository,
	stacks *repositories.StackRepository,
	volumes *repositories.VolumeRepository,
) *Service {
	exists := func(kind string, id uint) (bool, error) {
		switch kind {
		case drift.OwnerApp:
			return apps.ExistsByID(id)
		case drift.OwnerDatabase:
			return dbs.ExistsByID(id)
		case drift.OwnerVolume:
			return volumes.ExistsByID(id)
		case drift.OwnerStack:
			return stacks.ExistsByID(id)
		default:
			// Unknown owner kind: treat as existing so we never remove something we
			// cannot positively classify as orphaned.
			return true, nil
		}
	}
	return &Service{clients: clients, apps: apps, exists: exists, volumeNamed: volumes.ExistsByDockerName}
}

// Report is the full housekeeping analysis for a node: disk usage, the safe
// reclaim breakdown, and the drift table. Pure read — no mutations.
type Report struct {
	NodeID  uint             `json:"node_id"`
	Disk    docker.DiskUsage `json:"disk"`
	Reclaim ReclaimBreakdown `json:"reclaim"`
	Drift   DriftSummary     `json:"drift"`
}

// ReclaimBreakdown is the safe reclaimable set offered in the Reclaim UI. Only
// always-safe categories are surfaced here; unused (non-dangling) image pruning
// is gated behind the referenced-image guard and is not offered yet.
type ReclaimBreakdown struct {
	DanglingImages CategoryStat `json:"dangling_images"`
	BuildCache     CategoryStat `json:"build_cache"`
}

// CategoryStat is a count + reclaimable bytes for one reclaim category.
type CategoryStat struct {
	Count int   `json:"count"`
	Bytes int64 `json:"bytes"`
}

// DriftSummary groups drift items by class.
type DriftSummary struct {
	Orphans   []drift.Item `json:"orphans"`
	Missing   []drift.Item `json:"missing"`
	Untracked []drift.Item `json:"untracked"`
}

// Analyze joins live Docker against the DB and builds the full report. Pure read.
func (s *Service) Analyze(ctx context.Context, nodeID uint) (*Report, error) {
	dc, err := s.clients.For(nodeID)
	if err != nil {
		return nil, err
	}
	rep := &Report{NodeID: nodeID}

	if du, derr := dc.DiskUsage(ctx); derr == nil {
		rep.Disk = du
		rep.Reclaim.BuildCache = CategoryStat{Count: du.BuildCache.Count - du.BuildCache.Active, Bytes: du.BuildCache.Reclaimable}
	}
	if imgs, ierr := dc.ListImages(ctx); ierr == nil {
		for _, im := range imgs {
			if im.Dangling {
				rep.Reclaim.DanglingImages.Count++
				rep.Reclaim.DanglingImages.Bytes += im.Size
			}
		}
	}

	summary, derr := s.analyzeDrift(ctx, dc, nodeID)
	if derr != nil {
		return nil, derr
	}
	rep.Drift = *summary
	return rep, nil
}

// analyzeDrift classifies every live container + volume on the node against the
// DB by the miabi.* label scheme, then derives missing apps (record exists,
// nothing running for it).
func (s *Service) analyzeDrift(ctx context.Context, dc docker.Client, nodeID uint) (*DriftSummary, error) {
	containers, err := dc.ListContainers(ctx, true) // all states
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	volumes, err := dc.ListVolumes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}

	out := &DriftSummary{Orphans: []drift.Item{}, Missing: []drift.Item{}, Untracked: []drift.Item{}}
	// app IDs that have at least one live container, so a record with none can be
	// flagged missing.
	liveApps := map[uint]bool{}

	for i := range containers {
		c := containers[i]
		kind, id, ok := drift.OwnerOf(c.Labels)
		switch {
		case !isManaged(c.Labels):
			out.Untracked = append(out.Untracked, drift.Item{
				Class: drift.ClassUntracked, Kind: "container", Ref: c.ID,
				Name: containerName(c), Image: c.Image, State: c.State, Action: drift.ActionImport,
			})
		case !ok:
			// Managed platform infra (gateway/redis), a job, or a stack-only
			// container: tracked, never an orphan target. Skip.
			continue
		default:
			if kind == drift.OwnerApp {
				liveApps[id] = true
			}
			exists, eerr := s.exists(kind, id)
			if eerr != nil {
				return nil, eerr
			}
			if !exists {
				out.Orphans = append(out.Orphans, drift.Item{
					Class: drift.ClassOrphan, Kind: "container", Ref: c.ID, Name: containerName(c),
					Image: c.Image, State: c.State, OwnerKind: kind, OwnerID: id, Action: drift.ActionRemove,
				})
			}
		}
	}

	for i := range volumes {
		v := volumes[i]
		// Unmanaged volumes are an import concern, not drift we remove, and infra
		// volumes (role-labelled) are managed through their own pages.
		if isPlatformInfra(v.Labels) {
			continue
		}
		kind, id, ok := drift.VolumeOwner(v.Labels)
		if !ok {
			orphan, oerr := s.unlabelledVolumeOrphan(v.Name)
			if oerr != nil {
				return nil, oerr
			}
			if orphan {
				out.Orphans = append(out.Orphans, drift.Item{
					Class: drift.ClassOrphan, Kind: "volume", Ref: v.Name, Name: v.Name,
					OwnerKind: drift.OwnerVolume, Action: drift.ActionRemove,
				})
			}
			continue
		}
		exists, eerr := s.exists(kind, id)
		if eerr != nil {
			return nil, eerr
		}
		if !exists {
			out.Orphans = append(out.Orphans, drift.Item{
				Class: drift.ClassOrphan, Kind: "volume", Ref: v.Name, Name: v.Name,
				OwnerKind: kind, OwnerID: id, Action: drift.ActionRemove,
			})
		}
	}

	// Swarm config objects whose owning Config row is gone. Docker refuses to
	// remove one a service still references, so an in-use object can never be
	// swept out from under a running task even if the classification is stale.
	if cfgs, cerr := dc.ListManagedConfigs(ctx); cerr == nil {
		for _, c := range cfgs {
			if c.Config == "" || c.Workspace == "" {
				continue
			}
			wsID, ok := parseID(c.Workspace)
			if !ok {
				continue
			}
			live, lerr := s.configExists(wsID, c.Config)
			if lerr != nil || live {
				continue
			}
			out.Orphans = append(out.Orphans, drift.Item{
				Class: drift.ClassOrphan, Kind: "config", Ref: c.ID, Name: c.Name,
				OwnerKind: drift.OwnerConfig, OwnerID: 0, Action: drift.ActionRemove,
			})
		}
	}

	// Missing: apps placed on this node, expected running, with nothing running for them.
	apps, aerr := s.apps.ListByServer(nodeID)
	if aerr != nil {
		return nil, aerr
	}
	for i := range apps {
		a := &apps[i]
		if a.Status != models.AppStatusRunning {
			continue
		}
		if a.RuntimeKind == models.RuntimeService {
			if s.serviceMissing(ctx, a) {
				out.Missing = append(out.Missing, drift.Item{
					Class: drift.ClassMissing, Kind: "service", Ref: fmt.Sprintf("app:%d", a.ID),
					Name: a.Name, OwnerKind: drift.OwnerApp, OwnerID: a.ID, Action: drift.ActionRedeploy,
				})
			}
			continue
		}
		if liveApps[a.ID] {
			continue
		}
		out.Missing = append(out.Missing, drift.Item{
			Class: drift.ClassMissing, Kind: "container", Ref: fmt.Sprintf("app:%d", a.ID),
			Name: a.Name, OwnerKind: drift.OwnerApp, OwnerID: a.ID, Action: drift.ActionRedeploy,
		})
	}
	return out, nil
}

// serviceMissing reports whether a service app's swarm service is gone. Its tasks run wherever Swarm
// placed them, often on another node, so this node's containers say nothing about it; only its cluster's
// manager can. An unreachable manager, or no lookup wired, is unknown — never missing.
func (s *Service) serviceMissing(ctx context.Context, app *models.Application) bool {
	if s.swarms == nil {
		return false
	}
	mgr, err := s.swarms.Manager(ctx, app.ClusterID)
	if err != nil {
		return false
	}
	_, err = mgr.ServiceInspect(ctx, node.AppAlias(app))
	return errors.Is(err, docker.ErrNotFound)
}

// unlabelledVolumeOrphan classifies a volume that names no owner. Volumes storage created before it
// wrote io.miabi.volume keep only their Miabi name, and volume rows are hard-deleted, so a Miabi-named
// volume no row claims is one deleted in Miabi.
func (s *Service) unlabelledVolumeOrphan(name string) (bool, error) {
	if s.volumeNamed == nil || !isMiabiVolumeName(name) {
		return false, nil
	}
	claimed, err := s.volumeNamed(name)
	if err != nil {
		return false, err
	}
	return !claimed, nil
}

// configExists reports whether a workspace still has a config by that name; a
// config object whose row is gone is what makes the object an orphan.
func (s *Service) configExists(workspaceID uint, name string) (bool, error) {
	if s.configs == nil {
		return true, nil // no resolver wired: never classify as an orphan
	}
	_, err := s.configs.GetByName(workspaceID, name)
	if err != nil {
		return false, nil
	}
	return true, nil
}

func containerName(c docker.Container) string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return c.ID
}
