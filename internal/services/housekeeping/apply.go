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

package housekeeping

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/drift"
	"github.com/miabi-io/miabi/internal/models"
)

// Always re-validated against a fresh analysis so a crafted ref can't smuggle in a managed or infra resource.
type Selection struct {
	Reclaim ReclaimSelection `json:"reclaim"`
	// Only refs that re-confirm as orphans in a fresh analysis are acted on.
	Orphans []ResourceRef `json:"orphans"`
	// Only refs that re-confirm as missing are acted on, so a selection never starts something already running.
	Missing []ResourceRef `json:"missing"`
}

// ReclaimSelection picks which reclaim categories to run.
type ReclaimSelection struct {
	DanglingImages bool `json:"dangling_images"`
	BuildCache     bool `json:"build_cache"`
	UnusedImages   bool `json:"unused_images"`
	UnusedVolumes  bool `json:"unused_volumes"`
}

// ResourceRef identifies a drift item to act on.
type ResourceRef struct {
	Kind string `json:"kind"` // container | service | volume | config
	Ref  string `json:"ref"`  // container ID, service name, volume name or config ID
}

// Plan is the dry run after the safety contract is applied; preview equals what is applied.
type Plan struct {
	Reclaim        ReclaimSelection `json:"reclaim"`
	DanglingImages CategoryStat     `json:"dangling_images"`
	BuildCache     CategoryStat     `json:"build_cache"`
	UnusedImages   CategoryStat     `json:"unused_images"`
	UnusedVolumes  CategoryStat     `json:"unused_volumes"`
	// Itemized so the preview can be checked item by item rather than trusted as a count.
	UnusedImageRefs   []string     `json:"unused_image_refs,omitempty"`
	UnusedVolumeNames []string     `json:"unused_volume_names,omitempty"`
	Orphans           []drift.Item `json:"orphans"`
	// Missing are the workloads that would be redeployed.
	Missing        []drift.Item `json:"missing"`
	EstimatedBytes int64        `json:"estimated_bytes"`
}

type Result struct {
	ImagesDeleted   int          `json:"images_deleted"`
	ImagesBytes     int64        `json:"images_reclaimed_bytes"`
	BuildCacheBytes int64        `json:"build_cache_reclaimed_bytes"`
	VolumesDeleted  int          `json:"volumes_deleted"`
	VolumesBytes    int64        `json:"volumes_reclaimed_bytes"`
	OrphansRemoved  []drift.Item `json:"orphans_removed"`
	// Redeployed are the missing workloads a deploy was queued for.
	Redeployed []drift.Item `json:"redeployed"`
	Errors     []string     `json:"errors,omitempty"`
}

// Uses the ordinary deploy path, so its data guard still refuses an app whose volume is gone.
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

func (s *Service) Plan(ctx context.Context, nodeID uint, sel Selection) (*Plan, error) {
	rep, unusedImgs, unusedVols, err := s.analyze(ctx, nodeID)
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
	if sel.Reclaim.UnusedImages {
		p.UnusedImages = rep.Reclaim.UnusedImages
		p.EstimatedBytes += rep.Reclaim.UnusedImages.Bytes
		p.UnusedImageRefs = imageRefsOf(unusedImgs)
	}
	if sel.Reclaim.UnusedVolumes {
		p.UnusedVolumes = rep.Reclaim.UnusedVolumes
		p.EstimatedBytes += rep.Reclaim.UnusedVolumes.Bytes
		for _, v := range unusedVols {
			p.UnusedVolumeNames = append(p.UnusedVolumeNames, v.DockerName)
		}
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

// Apply removes only fresh-confirmed orphans; a single failure does not abort the batch.
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
	if sel.Reclaim.UnusedImages || sel.Reclaim.UnusedVolumes {
		s.reclaimUnused(ctx, dc, sel.Reclaim, res)
	}

	if len(sel.Missing) > 0 {
		s.redeployMissing(ctx, dc, nodeID, sel, res)
	}

	if len(sel.Orphans) > 0 {
		// Re-confirm against fresh state so a selection can never remove a non-orphan.
		summary, derr := s.analyzeDrift(ctx, dc, nodeID)
		if derr != nil {
			return nil, derr
		}
		confirmed := orphanIndex(summary.Orphans)
		items := make([]drift.Item, 0, len(sel.Orphans))
		for _, ref := range sel.Orphans {
			if item, ok := confirmed[refKey(ref.Kind, ref.Ref)]; ok {
				items = append(items, item) // anything else is no longer an orphan: silently skip
			}
		}
		// Docker can't remove an in-use volume, so remove the service, then its container, then the volume.
		sort.SliceStable(items, func(i, j int) bool { return removalOrder(items[i].Kind) < removalOrder(items[j].Kind) })
		for _, item := range items {
			if rmErr := s.removeOrphan(ctx, dc, nodeID, item); rmErr != nil {
				res.Errors = append(res.Errors, item.Kind+" "+item.Ref+": "+rmErr.Error())
				continue
			}
			res.OrphansRemoved = append(res.OrphansRemoved, item)
		}
	}
	return res, nil
}

// Never uses the daemon's prune: the guard, not Docker's idea of unused, decides.
func (s *Service) reclaimUnused(ctx context.Context, dc docker.Client, sel ReclaimSelection, res *Result) {
	du, err := dc.DiskUsage(ctx)
	if err != nil {
		res.Errors = append(res.Errors, "measure node: "+err.Error())
		return
	}
	images, volumes, err := s.resolveUnused(ctx, dc, du.VolumeItems)
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
		return
	}
	if sel.UnusedImages {
		if len(s.imageRefs) == 0 {
			res.Errors = append(res.Errors, "reclaiming unused images is not available on this build")
		}
		for _, im := range images {
			// Never force: an image Docker finds a reference for since the guard ran is one the guard would have kept.
			if rmErr := dc.RemoveImage(ctx, im.ID, false); rmErr != nil {
				res.Errors = append(res.Errors, "image "+imageName(im)+": "+rmErr.Error())
				continue
			}
			res.ImagesDeleted++
			res.ImagesBytes += unsharedBytes(im)
		}
	}
	if sel.UnusedVolumes {
		for _, v := range volumes {
			// Never force either: force would remove a volume a container picked up since the guard ran.
			if rmErr := dc.RemoveVolume(ctx, v.DockerName, false); rmErr != nil {
				res.Errors = append(res.Errors, "volume "+v.DockerName+": "+rmErr.Error())
				continue
			}
			res.VolumesDeleted++
			res.VolumesBytes += v.Bytes
		}
	}
}

// imageRefsOf names each image by its first tag, falling back to its id.
func imageRefsOf(imgs []docker.Image) []string {
	out := make([]string, 0, len(imgs))
	for _, im := range imgs {
		out = append(out, imageName(im))
	}
	return out
}

func imageName(im docker.Image) string {
	if len(im.RepoTags) > 0 {
		return im.RepoTags[0]
	}
	return im.ID
}

// Uses the ordinary deploy path, so an app whose data volume is gone is refused rather than started empty.
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

// Force because the orphan's DB record is already gone, so the admin's reclaim is authoritative.
func (s *Service) removeOrphan(ctx context.Context, dc docker.Client, nodeID uint, item drift.Item) error {
	switch item.Kind {
	case "container":
		return dc.RemoveContainer(ctx, item.Ref, true)
	case "service":
		// Swarm reschedules a task whose container is removed, so only removing the service on a manager stops it.
		mgr, err := s.swarmManagerFor(ctx, nodeID)
		if err != nil {
			return err
		}
		return mgr.ServiceRemove(ctx, item.Ref)
	case "volume":
		return dc.RemoveVolume(ctx, item.Ref, true)
	case "config":
		// Docker refuses to remove a config a service still references, so a stale classification is harmless.
		return dc.RemoveConfig(ctx, item.Ref)
	default:
		return fmt.Errorf("unsupported orphan kind %q", item.Kind)
	}
}

// removalOrder ranks orphan kinds so an owner is always removed before what it holds.
func removalOrder(kind string) int {
	switch kind {
	case "service":
		return 0
	case "container":
		return 1
	case "volume":
		return 2
	default:
		return 3
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
