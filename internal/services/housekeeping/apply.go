// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package housekeeping

import (
	"context"
	"fmt"
	"strconv"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/drift"
	"github.com/miabi-io/miabi/internal/models"
)

// Selection is what an admin chose to reclaim and/or reconcile. It is always
// re-validated against a fresh analysis before anything is removed — the client
// cannot smuggle in a managed/infra resource by crafting a ref.
type Selection struct {
	Reclaim ReclaimSelection `json:"reclaim"`
	// Orphans are the drift items to remove, identified by kind+ref. Only refs
	// that re-confirm as orphans in a fresh analysis are acted on.
	Orphans []ResourceRef `json:"orphans"`
	// Missing are the workloads to redeploy, identified by kind+ref. Same contract as Orphans: only refs that
	// re-confirm as missing are acted on, so a selection can never start something that is already running.
	Missing []ResourceRef `json:"missing"`
}

// ReclaimSelection picks which safe reclaim categories to run.
type ReclaimSelection struct {
	DanglingImages bool `json:"dangling_images"`
	BuildCache     bool `json:"build_cache"`
}

// ResourceRef identifies a drift item to act on.
type ResourceRef struct {
	Kind string `json:"kind"` // container | volume | config
	Ref  string `json:"ref"`  // container ID, volume name or config ID
}

// Plan is the dry-run preview of a Selection: exactly what would be reclaimed
// and removed, after the safety contract is applied. preview == applied.
type Plan struct {
	Reclaim        ReclaimSelection `json:"reclaim"`
	DanglingImages CategoryStat     `json:"dangling_images"`
	BuildCache     CategoryStat     `json:"build_cache"`
	Orphans        []drift.Item     `json:"orphans"`
	// Missing are the workloads that would be redeployed.
	Missing        []drift.Item `json:"missing"`
	EstimatedBytes int64        `json:"estimated_bytes"`
}

// Result is the outcome of an Apply: bytes freed per category and the orphans
// removed. The caller audits each removal from this.
type Result struct {
	ImagesDeleted   int          `json:"images_deleted"`
	ImagesBytes     int64        `json:"images_reclaimed_bytes"`
	BuildCacheBytes int64        `json:"build_cache_reclaimed_bytes"`
	OrphansRemoved  []drift.Item `json:"orphans_removed"`
	// Redeployed are the missing workloads a deploy was queued for.
	Redeployed []drift.Item `json:"redeployed"`
	Errors     []string     `json:"errors,omitempty"`
}

// Redeployer queues a deploy for an app that should be running but is not. Satisfied by
// *application.Service: the ordinary deploy path, so the data guard there still refuses an app whose volume
// is gone, and nothing here has to know about that.
type Redeployer interface {
	ReconcileRedeploy(app *models.Application, reason string) (*models.Deployment, error)
}

// appFinder resolves the app a missing drift item names.
type appFinder interface {
	FindByID(id uint) (*models.Application, error)
}

// SetRedeployer wires redeploying a missing workload. Without it, missing items stay report-only.
func (s *Service) SetRedeployer(r Redeployer, apps appFinder) {
	s.redeployer, s.appsByID = r, apps
}

// Plan re-analyzes the node and intersects the selection with what is actually
// present and safe, returning the itemized dry-run. Nothing is mutated.
func (s *Service) Plan(ctx context.Context, nodeID uint, sel Selection) (*Plan, error) {
	rep, err := s.Analyze(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	p := &Plan{Reclaim: sel.Reclaim, Orphans: []drift.Item{}, Missing: []drift.Item{}}
	if sel.Reclaim.DanglingImages {
		p.DanglingImages = rep.Reclaim.DanglingImages
		p.EstimatedBytes += rep.Reclaim.DanglingImages.Bytes
	}
	if sel.Reclaim.BuildCache {
		p.BuildCache = rep.Reclaim.BuildCache
		p.EstimatedBytes += rep.Reclaim.BuildCache.Bytes
	}
	confirmed := orphanIndex(rep.Drift.Orphans)
	for _, ref := range sel.Orphans {
		if item, ok := confirmed[refKey(ref.Kind, ref.Ref)]; ok {
			p.Orphans = append(p.Orphans, item)
		}
	}
	missing := orphanIndex(rep.Drift.Missing)
	for _, ref := range sel.Missing {
		if item, ok := missing[refKey(ref.Kind, ref.Ref)]; ok {
			p.Missing = append(p.Missing, item)
		}
	}
	return p, nil
}

// Apply executes the selection. Reclaim runs the safe prunes; reconcile removes only resources that
// re-confirm as orphans in a fresh analysis, so managed, infra and self resources never survive that
// filter. Each removal is reported for auditing, and a single failure does not abort the batch.
func (s *Service) Apply(ctx context.Context, nodeID uint, sel Selection) (*Result, error) {
	dc, err := s.clients.For(nodeID)
	if err != nil {
		return nil, err
	}
	res := &Result{OrphansRemoved: []drift.Item{}, Redeployed: []drift.Item{}}

	if sel.Reclaim.DanglingImages {
		if rep, perr := dc.PruneImages(ctx, docker.PruneImagesOptions{Dangling: true}); perr != nil {
			res.Errors = append(res.Errors, "prune dangling images: "+perr.Error())
		} else {
			res.ImagesDeleted = len(rep.ItemsDeleted)
			res.ImagesBytes = rep.SpaceReclaimed
		}
	}
	if sel.Reclaim.BuildCache {
		if rep, perr := dc.PruneBuildCache(ctx); perr != nil {
			res.Errors = append(res.Errors, "prune build cache: "+perr.Error())
		} else {
			res.BuildCacheBytes = rep.SpaceReclaimed
		}
	}

	if len(sel.Missing) > 0 {
		s.redeployMissing(ctx, dc, nodeID, sel, res)
	}

	if len(sel.Orphans) > 0 {
		// Re-confirm orphan status against fresh state so a selection can never
		// remove a resource that is no longer (or never was) an orphan.
		summary, derr := s.analyzeDrift(ctx, dc, nodeID)
		if derr != nil {
			return nil, derr
		}
		confirmed := orphanIndex(summary.Orphans)
		for _, ref := range sel.Orphans {
			item, ok := confirmed[refKey(ref.Kind, ref.Ref)]
			if !ok {
				continue // not an orphan anymore; silently skip
			}
			if rmErr := s.removeOrphan(ctx, dc, item); rmErr != nil {
				res.Errors = append(res.Errors, item.Kind+" "+item.Ref+": "+rmErr.Error())
				continue
			}
			res.OrphansRemoved = append(res.OrphansRemoved, item)
		}
	}
	return res, nil
}

// redeployMissing queues a deploy for each selected workload that re-confirms as missing. It goes through the
// ordinary deploy path, so an app whose data volume is gone is refused there rather than started on an empty
// one — the queue, the per-app lock and every guard are the same as a deploy a user asks for.
func (s *Service) redeployMissing(ctx context.Context, dc docker.Client, nodeID uint, sel Selection, res *Result) {
	if s.redeployer == nil || s.appsByID == nil {
		res.Errors = append(res.Errors, "redeploying a missing workload is not available on this build")
		return
	}
	summary, err := s.analyzeDrift(ctx, dc, nodeID)
	if err != nil {
		res.Errors = append(res.Errors, "re-check drift: "+err.Error())
		return
	}
	confirmed := orphanIndex(summary.Missing)
	for _, ref := range sel.Missing {
		item, ok := confirmed[refKey(ref.Kind, ref.Ref)]
		if !ok {
			continue // running again since the preview; silently skip
		}
		app, ferr := s.appsByID.FindByID(item.OwnerID)
		if ferr != nil {
			res.Errors = append(res.Errors, item.Name+": "+ferr.Error())
			continue
		}
		if _, derr := s.redeployer.ReconcileRedeploy(app, "housekeeping: its workload was missing on node "+strconv.FormatUint(uint64(nodeID), 10)); derr != nil {
			res.Errors = append(res.Errors, item.Name+": "+derr.Error())
			continue
		}
		res.Redeployed = append(res.Redeployed, item)
	}
}

// removeOrphan deletes a confirmed orphan. Force is used because an orphan's DB
// record is already gone, so the admin's reclaim is the authoritative intent.
func (s *Service) removeOrphan(ctx context.Context, dc docker.Client, item drift.Item) error {
	switch item.Kind {
	case "container":
		return dc.RemoveContainer(ctx, item.Ref, true)
	case "volume":
		return dc.RemoveVolume(ctx, item.Ref, true)
	case "config":
		// Docker refuses to remove a config a service still references, so a stale
		// classification cannot pull one out from under a running task.
		return dc.RemoveConfig(ctx, item.Ref)
	default:
		return fmt.Errorf("unsupported orphan kind %q", item.Kind)
	}
}

// orphanIndex keys orphan items by kind+ref for O(1) re-confirmation.
func orphanIndex(orphans []drift.Item) map[string]drift.Item {
	m := make(map[string]drift.Item, len(orphans))
	for _, o := range orphans {
		m[refKey(o.Kind, o.Ref)] = o
	}
	return m
}

func refKey(kind, ref string) string { return kind + "\x00" + ref }
