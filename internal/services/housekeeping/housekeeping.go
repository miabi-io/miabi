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

// Package housekeeping reconciles a node's Docker state against Miabi's intent: disk report, safe prune, drift.
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

type appLister interface {
	ListByServer(serverID uint) ([]models.Application, error)
}

// SwarmManagers resolves the client that drives a cluster's swarm. Implemented by the cluster service.
type SwarmManagers interface {
	Manager(ctx context.Context, clusterID uint) (docker.Client, error)
}

// A soft-deleted record reads as absent, which is the orphan condition.
type recordExister func(kind string, id uint) (bool, error)

// Service computes housekeeping reports and applies reclaim/reconcile actions.
type Service struct {
	clients Clients
	apps    appLister
	exists  recordExister
	// nil never classifies an orphan.
	configs configLookup
	// For volumes created before they were labelled; nil never classifies an orphan.
	volumeNamed func(dockerName string) (bool, error)
	// Service tasks may run on any node of the cluster; nil never reports a service app missing.
	swarms SwarmManagers
	// Removal goes through the cluster's manager because the node's own engine may be a worker.
	nodes nodeLookup
	// nil leaves missing rows report-only.
	redeployer Redeployer
	appsByID   appFinder
	// imageRefs is the referenced-image guard. Empty never offers unused-image reclaim.
	imageRefs []ImageRefSource
}

// configLookup resolves a config by name within a workspace.
type configLookup interface {
	GetByName(workspaceID uint, name string) (*models.Config, error)
}

// SetConfigs wires the config lookup used to classify swarm config objects.
func (s *Service) SetConfigs(c configLookup) { s.configs = c }

// SetSwarmManagers wires the cluster manager lookup used to check service apps.
func (s *Service) SetSwarmManagers(m SwarmManagers) { s.swarms = m }

// nodeLookup resolves a node record, for the cluster it belongs to.
type nodeLookup interface {
	Get(id uint) (*models.Server, error)
}

// SetNodes wires the node lookup used to reach a node's swarm manager.
func (s *Service) SetNodes(n nodeLookup) { s.nodes = n }

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
			// Treat unknown owner kinds as existing so we never remove what we can't positively classify.
			return true, nil
		}
	}
	return &Service{clients: clients, apps: apps, exists: exists, volumeNamed: volumes.ExistsByDockerName}
}

type Report struct {
	NodeID  uint             `json:"node_id"`
	Disk    docker.DiskUsage `json:"disk"`
	Reclaim ReclaimBreakdown `json:"reclaim"`
	Drift   DriftSummary     `json:"drift"`
}

// Unused images and volumes are resolved to an explicit guarded list, so neither can take what a record needs.
type ReclaimBreakdown struct {
	DanglingImages CategoryStat `json:"dangling_images"`
	BuildCache     CategoryStat `json:"build_cache"`
	UnusedImages   CategoryStat `json:"unused_images"`
	UnusedVolumes  CategoryStat `json:"unused_volumes"`
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
	rep, _, _, err := s.analyze(ctx, nodeID)
	return rep, err
}

// Returns the guarded sets so a preview can name them without measuring the node again.
func (s *Service) analyze(ctx context.Context, nodeID uint) (*Report, []docker.Image, []docker.VolumeUsage, error) {
	dc, err := s.clients.For(nodeID)
	if err != nil {
		return nil, nil, nil, err
	}
	rep := &Report{NodeID: nodeID}

	images, volumes, rerr := s.reclaimable(ctx, dc, rep)
	if rerr != nil {
		return nil, nil, nil, rerr
	}

	summary, derr := s.analyzeDrift(ctx, dc, nodeID)
	if derr != nil {
		return nil, nil, nil, derr
	}
	rep.Drift = *summary
	return rep, images, volumes, nil
}

// Docker failures are tolerated, but an unevaluable guard is an error: a half-evaluated guard under-protects.
func (s *Service) reclaimable(ctx context.Context, dc docker.Client, rep *Report) ([]docker.Image, []docker.VolumeUsage, error) {
	var volumeUsage []docker.VolumeUsage
	if du, err := dc.DiskUsage(ctx); err == nil {
		volumeUsage = du.VolumeItems
		du.VolumeItems = nil // the report carries the totals, not a row per volume
		rep.Disk = du
		rep.Reclaim.BuildCache = CategoryStat{Count: du.BuildCache.Count - du.BuildCache.Active, Bytes: du.BuildCache.Reclaimable}
	}

	if imgs, ierr := dc.ListImages(ctx); ierr == nil {
		for _, im := range imgs {
			if im.Dangling {
				rep.Reclaim.DanglingImages.Count++
				rep.Reclaim.DanglingImages.Bytes += unsharedBytes(im)
			}
		}
	}

	images, volumes, err := s.resolveUnused(ctx, dc, volumeUsage)
	if err != nil {
		return nil, nil, err
	}
	for _, im := range images {
		rep.Reclaim.UnusedImages.Count++
		rep.Reclaim.UnusedImages.Bytes += unsharedBytes(im)
	}
	for _, v := range volumes {
		rep.Reclaim.UnusedVolumes.Count++
		rep.Reclaim.UnusedVolumes.Bytes += v.Bytes
	}
	return images, volumes, nil
}

// Shared by preview and apply, so what an admin confirms is what the guard allows at removal time.
func (s *Service) resolveUnused(ctx context.Context, dc docker.Client, volumeUsage []docker.VolumeUsage) ([]docker.Image, []docker.VolumeUsage, error) {
	referenced, err := s.referencedImages()
	if err != nil {
		return nil, nil, fmt.Errorf("referenced images: %w", err)
	}
	var images []docker.Image
	if imgs, ierr := dc.ListImages(ctx); ierr == nil {
		images = unusedImages(imgs, referenced)
	}
	if len(volumeUsage) == 0 {
		return images, nil, nil
	}
	vols, verr := dc.ListVolumes(ctx)
	if verr != nil {
		return nil, nil, fmt.Errorf("list volumes: %w", verr)
	}
	volumes, uerr := unusedVolumes(vols, volumeUsage, s.volumeNamed)
	if uerr != nil {
		return nil, nil, fmt.Errorf("unused volumes: %w", uerr)
	}
	return images, volumes, nil
}

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
	liveApps := map[uint]bool{}
	// Swarm services already reported once, so N task containers yield one row.
	seenServices := map[string]bool{}

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
			continue
		default:
			if kind == drift.OwnerApp {
				liveApps[id] = true
			}
			exists, eerr := s.exists(kind, id)
			if eerr != nil {
				return nil, eerr
			}
			if exists {
				continue
			}
			// A swarm task's container is disposable: Swarm replaces it, so the service is the orphan.
			if svc := swarmServiceName(c.Labels); svc != "" {
				if seenServices[svc] {
					continue
				}
				seenServices[svc] = true
				out.Orphans = append(out.Orphans, drift.Item{
					Class: drift.ClassOrphan, Kind: "service", Ref: svc, Name: svc,
					Image: c.Image, State: c.State, OwnerKind: kind, OwnerID: id, Action: drift.ActionRemove,
				})
				continue
			}
			out.Orphans = append(out.Orphans, drift.Item{
				Class: drift.ClassOrphan, Kind: "container", Ref: c.ID, Name: containerName(c),
				Image: c.Image, State: c.State, OwnerKind: kind, OwnerID: id, Action: drift.ActionRemove,
			})
		}
	}

	for i := range volumes {
		v := volumes[i]
		// Unmanaged volumes are an import concern, and infra volumes are managed through their own pages.
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

	// Docker refuses to remove a config a service still references, so a stale classification is harmless.
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

// A worker can't act on services, so the node's own engine is never a fallback.
func (s *Service) swarmManagerFor(ctx context.Context, nodeID uint) (docker.Client, error) {
	if s.swarms == nil || s.nodes == nil {
		return nil, errors.New("swarm service removal is not available on this build")
	}
	srv, err := s.nodes.Get(nodeID)
	if err != nil {
		return nil, err
	}
	return s.swarms.Manager(ctx, srv.ClusterID)
}

// Only the cluster manager knows; an unreachable manager or no lookup is unknown, never missing.
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

// Volume rows are hard-deleted, so a Miabi-named unlabelled volume no row claims was deleted in Miabi.
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
