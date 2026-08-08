// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
)

// ListImages enumerates the node's local images, flagging dangling (untagged)
// ones and carrying the container-reference count so a report can tell which are
// reclaimable. SharedSize/Containers are computed by the daemon here.
func (e *engineClient) ListImages(ctx context.Context) ([]Image, error) {
	list, err := e.cli.ImageList(ctx, image.ListOptions{All: false, SharedSize: true})
	if err != nil {
		return nil, err
	}
	out := make([]Image, 0, len(list))
	for _, im := range list {
		out = append(out, Image{
			ID:          im.ID,
			RepoTags:    im.RepoTags,
			RepoDigests: im.RepoDigests,
			Size:        im.Size,
			SharedSize:  im.SharedSize,
			Created:     im.Created,
			Containers:  im.Containers,
			Dangling:    isDanglingImage(im.RepoTags),
			Labels:      im.Labels,
		})
	}
	return out, nil
}

// isDanglingImage reports whether an image has no usable tag — no RepoTags at all, or only the
// placeholder "<none>:<none>". Such images are always safe to prune.
func isDanglingImage(repoTags []string) bool {
	for _, t := range repoTags {
		if t != "" && t != "<none>:<none>" {
			return false
		}
	}
	return true
}

// DiskUsage returns a `docker system df`-style breakdown for the node. Per category it reports
// total bytes, reclaimable bytes (an upper bound that ignores cross-image layer sharing, as the
// Docker CLI also does), and how many items are in use.
func (e *engineClient) DiskUsage(ctx context.Context) (DiskUsage, error) {
	du, err := e.cli.DiskUsage(ctx, types.DiskUsageOptions{})
	if err != nil {
		return DiskUsage{}, err
	}
	var out DiskUsage

	out.Images.TotalBytes = du.LayersSize // unique on-disk size across all images
	for _, im := range du.Images {
		out.Images.Count++
		switch {
		case im.Containers > 0:
			out.Images.Active++
		default: // unused (0) or not-computed (-1): treat as reclaimable
			out.Images.Reclaimable += im.Size
		}
	}

	for _, ct := range du.Containers {
		out.Containers.Count++
		out.Containers.TotalBytes += ct.SizeRw
		if ct.State == "running" {
			out.Containers.Active++
		} else {
			out.Containers.Reclaimable += ct.SizeRw
		}
	}

	for _, v := range volumeUsageFrom(du.Volumes) {
		out.Volumes.Count++
		out.Volumes.TotalBytes += v.Bytes
		if v.RefCount > 0 {
			out.Volumes.Active++
		} else {
			out.Volumes.Reclaimable += v.Bytes
		}
	}

	for _, bc := range du.BuildCache {
		if bc.Shared {
			continue // shared records are counted under their owning record
		}
		out.BuildCache.Count++
		out.BuildCache.TotalBytes += bc.Size
		if bc.InUse {
			out.BuildCache.Active++
		} else {
			out.BuildCache.Reclaimable += bc.Size
		}
	}
	return out, nil
}

// VolumeUsage returns measured on-disk bytes per Docker volume name. Runs the
// daemon's `system df` filesystem walk, so it is sweep-only — never per read.
func (e *engineClient) VolumeUsage(ctx context.Context) ([]VolumeUsage, error) {
	du, err := e.cli.DiskUsage(ctx, types.DiskUsageOptions{})
	if err != nil {
		return nil, err
	}
	return volumeUsageFrom(du.Volumes), nil
}

// volumeUsageFrom projects the SDK volumes into our shape, skipping ones the daemon did not size
// (nil UsageData, or Size -1).
func volumeUsageFrom(vols []*volume.Volume) []VolumeUsage {
	out := make([]VolumeUsage, 0, len(vols))
	for _, v := range vols {
		if v == nil || v.UsageData == nil || v.UsageData.Size < 0 {
			continue
		}
		out = append(out, VolumeUsage{
			DockerName: v.Name,
			Bytes:      v.UsageData.Size,
			RefCount:   int(v.UsageData.RefCount),
		})
	}
	return out
}

// PruneImages reclaims images. With Dangling set it removes only untagged images (always safe);
// otherwise it removes every image no container references — callers must apply their
// referenced-image guard first. An optional Until age filter restricts it to older images.
func (e *engineClient) PruneImages(ctx context.Context, opts PruneImagesOptions) (PruneReport, error) {
	f := filters.NewArgs()
	if opts.Dangling {
		f.Add("dangling", "true")
	} else {
		f.Add("dangling", "false")
	}
	if opts.Until != "" {
		f.Add("until", opts.Until)
	}
	rep, err := e.cli.ImagesPrune(ctx, f)
	if err != nil {
		return PruneReport{}, err
	}
	out := PruneReport{SpaceReclaimed: int64(rep.SpaceReclaimed)}
	for _, d := range rep.ImagesDeleted {
		if d.Deleted != "" {
			out.ItemsDeleted = append(out.ItemsDeleted, d.Deleted)
		} else if d.Untagged != "" {
			out.ItemsDeleted = append(out.ItemsDeleted, d.Untagged)
		}
	}
	return out, nil
}

// PruneBuildCache reclaims the BuildKit build cache. It removes only unused
// records (no All flag), so an in-progress or referenced build is never touched.
func (e *engineClient) PruneBuildCache(ctx context.Context) (PruneReport, error) {
	rep, err := e.cli.BuildCachePrune(ctx, types.BuildCachePruneOptions{})
	if err != nil {
		return PruneReport{}, err
	}
	if rep == nil {
		return PruneReport{}, nil
	}
	return PruneReport{ItemsDeleted: rep.CachesDeleted, SpaceReclaimed: int64(rep.SpaceReclaimed)}, nil
}
