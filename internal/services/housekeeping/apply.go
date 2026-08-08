// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package housekeeping

import (
	"context"

	"github.com/miabi-io/miabi/internal/docker"
)

// Selection is what an admin chose to reclaim and/or reconcile. It is always
// re-validated against a fresh analysis before anything is removed — the client
// cannot smuggle in a managed/infra resource by crafting a ref.
type Selection struct {
	Reclaim ReclaimSelection `json:"reclaim"`
	// Orphans are the drift items to remove, identified by kind+ref. Only refs
	// that re-confirm as orphans in a fresh analysis are acted on.
	Orphans []ResourceRef `json:"orphans"`
}

// ReclaimSelection picks which safe reclaim categories to run.
type ReclaimSelection struct {
	DanglingImages bool `json:"dangling_images"`
	BuildCache     bool `json:"build_cache"`
}

// ResourceRef identifies a drift item to act on.
type ResourceRef struct {
	Kind string `json:"kind"` // container | volume
	Ref  string `json:"ref"`  // container ID or volume name
}

// Plan is the dry-run preview of a Selection: exactly what would be reclaimed
// and removed, after the safety contract is applied. preview == applied.
type Plan struct {
	Reclaim        ReclaimSelection `json:"reclaim"`
	DanglingImages CategoryStat     `json:"dangling_images"`
	BuildCache     CategoryStat     `json:"build_cache"`
	Orphans        []DriftItem      `json:"orphans"`
	EstimatedBytes int64            `json:"estimated_bytes"`
}

// Result is the outcome of an Apply: bytes freed per category and the orphans
// removed. The caller audits each removal from this.
type Result struct {
	ImagesDeleted   int         `json:"images_deleted"`
	ImagesBytes     int64       `json:"images_reclaimed_bytes"`
	BuildCacheBytes int64       `json:"build_cache_reclaimed_bytes"`
	OrphansRemoved  []DriftItem `json:"orphans_removed"`
	Errors          []string    `json:"errors,omitempty"`
}

// Plan re-analyzes the node and intersects the selection with what is actually
// present and safe, returning the itemized dry-run. Nothing is mutated.
func (s *Service) Plan(ctx context.Context, nodeID uint, sel Selection) (*Plan, error) {
	rep, err := s.Analyze(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	p := &Plan{Reclaim: sel.Reclaim, Orphans: []DriftItem{}}
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
	res := &Result{OrphansRemoved: []DriftItem{}}

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

	if len(sel.Orphans) > 0 {
		// Re-confirm orphan status against fresh state so a selection can never
		// remove a resource that is no longer (or never was) an orphan.
		drift, derr := s.analyzeDrift(ctx, dc, nodeID)
		if derr != nil {
			return nil, derr
		}
		confirmed := orphanIndex(drift.Orphans)
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

// removeOrphan deletes a confirmed orphan. Force is used because an orphan's DB
// record is already gone, so the admin's reclaim is the authoritative intent.
func (s *Service) removeOrphan(ctx context.Context, dc docker.Client, item DriftItem) error {
	switch item.Kind {
	case "container":
		return dc.RemoveContainer(ctx, item.Ref, true)
	case "volume":
		return dc.RemoveVolume(ctx, item.Ref, true)
	default:
		return nil
	}
}

// orphanIndex keys orphan items by kind+ref for O(1) re-confirmation.
func orphanIndex(orphans []DriftItem) map[string]DriftItem {
	m := make(map[string]DriftItem, len(orphans))
	for _, o := range orphans {
		m[refKey(o.Kind, o.Ref)] = o
	}
	return m
}

func refKey(kind, ref string) string { return kind + "\x00" + ref }
