// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package housekeeping reconciles a node's real Docker state against what Miabi intends, serving three
// faces of drift: a disk report, a safe categorized prune with a dry-run preview, and a reconcile that
// classifies orphan/missing/untracked by joining live Docker against the DB by the miabi.* labels.
package housekeeping

import (
	"context"
	"fmt"
	"strings"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
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

// recordExister reports whether the owning DB record for a managed resource (by
// kind + id) still exists. A soft-deleted record reads as absent — the orphan
// condition. Backed by the repos in production; a fake in tests.
type recordExister func(kind string, id uint) (bool, error)

// Service computes housekeeping reports and applies reclaim/reconcile actions.
type Service struct {
	clients Clients
	apps    appLister
	exists  recordExister
}

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
		case OwnerApp:
			return apps.ExistsByID(id)
		case OwnerDatabase:
			return dbs.ExistsByID(id)
		case OwnerVolume:
			return volumes.ExistsByID(id)
		case OwnerStack:
			return stacks.ExistsByID(id)
		default:
			// Unknown owner kind: treat as existing so we never remove something we
			// cannot positively classify as orphaned.
			return true, nil
		}
	}
	return &Service{clients: clients, apps: apps, exists: exists}
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
	Orphans   []DriftItem `json:"orphans"`
	Missing   []DriftItem `json:"missing"`
	Untracked []DriftItem `json:"untracked"`
}

// Drift classes.
const (
	ClassOrphan    = "orphan"    // managed label present, DB record gone — still running on the node
	ClassMissing   = "missing"   // DB record expects it live, no container present
	ClassUntracked = "untracked" // no miabi.* label — a hand-run resource
)

// Recommended actions per drift item.
const (
	ActionRemove   = "remove"   // orphan → delete the lingering resource
	ActionRedeploy = "redeploy" // missing → redeploy from the owning resource
	ActionImport   = "import"   // untracked → adopt via the existing import flow
)

// DriftItem is one resource that diverges from intent.
type DriftItem struct {
	Class     string `json:"class"`
	Kind      string `json:"kind"` // container | volume
	Ref       string `json:"ref"`  // container ID or volume name (the apply handle)
	Name      string `json:"name"`
	Image     string `json:"image,omitempty"`
	State     string `json:"state,omitempty"`
	OwnerKind string `json:"owner_kind,omitempty"` // app | database | volume | stack
	OwnerID   uint   `json:"owner_id,omitempty"`
	Action    string `json:"action"`
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

	drift, derr := s.analyzeDrift(ctx, dc, nodeID)
	if derr != nil {
		return nil, derr
	}
	rep.Drift = *drift
	return rep, nil
}

// analyzeDrift classifies every live container + volume on the node against the
// DB by the miabi.* label scheme, then derives missing apps (record exists,
// no live container).
func (s *Service) analyzeDrift(ctx context.Context, dc docker.Client, nodeID uint) (*DriftSummary, error) {
	containers, err := dc.ListContainers(ctx, true) // all states
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	volumes, err := dc.ListVolumes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}

	out := &DriftSummary{Orphans: []DriftItem{}, Missing: []DriftItem{}, Untracked: []DriftItem{}}
	// app IDs that have at least one live container, so a record with none can be
	// flagged missing.
	liveApps := map[uint]bool{}

	for i := range containers {
		c := containers[i]
		kind, id, ok := ownerOf(c.Labels)
		switch {
		case !isManaged(c.Labels):
			out.Untracked = append(out.Untracked, DriftItem{
				Class: ClassUntracked, Kind: "container", Ref: c.ID,
				Name: containerName(c), Image: c.Image, State: c.State, Action: ActionImport,
			})
		case !ok:
			// Managed platform infra (gateway/redis), a job, or a stack-only
			// container: tracked, never an orphan target. Skip.
			continue
		default:
			if kind == OwnerApp {
				liveApps[id] = true
			}
			exists, eerr := s.exists(kind, id)
			if eerr != nil {
				return nil, eerr
			}
			if !exists {
				out.Orphans = append(out.Orphans, DriftItem{
					Class: ClassOrphan, Kind: "container", Ref: c.ID, Name: containerName(c),
					Image: c.Image, State: c.State, OwnerKind: kind, OwnerID: id, Action: ActionRemove,
				})
			}
		}
	}

	for i := range volumes {
		v := volumes[i]
		// Only volumes that carry an explicit miabi.volume=<id> back-reference
		// are orphan-eligible; unmanaged volumes are an import concern, not drift we
		// remove. Infra volumes (role-labelled) are skipped.
		if isPlatformInfra(v.Labels) {
			continue
		}
		idStr, _ := docker.LabelValue(v.Labels, docker.LabelVolume)
		if idStr == "" {
			continue
		}
		id, ok := parseID(idStr)
		if !ok {
			continue
		}
		exists, eerr := s.exists(OwnerVolume, id)
		if eerr != nil {
			return nil, eerr
		}
		if !exists {
			out.Orphans = append(out.Orphans, DriftItem{
				Class: ClassOrphan, Kind: "volume", Ref: v.Name, Name: v.Name,
				OwnerKind: OwnerVolume, OwnerID: id, Action: ActionRemove,
			})
		}
	}

	// Missing: apps placed on this node, expected running, with no live container.
	apps, aerr := s.apps.ListByServer(nodeID)
	if aerr != nil {
		return nil, aerr
	}
	for i := range apps {
		a := &apps[i]
		if a.Status != models.AppStatusRunning {
			continue
		}
		if liveApps[a.ID] {
			continue
		}
		out.Missing = append(out.Missing, DriftItem{
			Class: ClassMissing, Kind: "container", Ref: fmt.Sprintf("app:%d", a.ID),
			Name: a.Name, OwnerKind: OwnerApp, OwnerID: a.ID, Action: ActionRedeploy,
		})
	}
	return out, nil
}

func containerName(c docker.Container) string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return c.ID
}
