// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"

	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

// ListImages enumerates the node's local images, flagging dangling (untagged)
// ones and carrying the container-reference count so a report can tell which are
// reclaimable. SharedSize/Containers are computed by the daemon here.
func (e *engineClient) ListImages(ctx context.Context) ([]Image, error) {
	res, err := e.cli.ImageList(ctx, client.ImageListOptions{All: false, SharedSize: true})
	if err != nil {
		return nil, err
	}
	out := make([]Image, 0, len(res.Items))
	for _, im := range res.Items {
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

// DiskUsage returns a `docker system df`-style breakdown for the node. The client pre-aggregates
// per-category counts and total/reclaimable bytes (the same numbers `docker system df` shows), so
// this is a direct projection.
func (e *engineClient) DiskUsage(ctx context.Context) (DiskUsage, error) {
	du, err := e.cli.DiskUsage(ctx, client.DiskUsageOptions{
		Containers: true, Images: true, Volumes: true, BuildCache: true,
	})
	if err != nil {
		return DiskUsage{}, err
	}
	return DiskUsage{
		Images: DiskUsageCategory{
			Count:       int(du.Images.TotalCount),
			Active:      int(du.Images.ActiveCount),
			TotalBytes:  du.Images.TotalSize,
			Reclaimable: du.Images.Reclaimable,
		},
		Containers: DiskUsageCategory{
			Count:       int(du.Containers.TotalCount),
			Active:      int(du.Containers.ActiveCount),
			TotalBytes:  du.Containers.TotalSize,
			Reclaimable: du.Containers.Reclaimable,
		},
		Volumes: DiskUsageCategory{
			Count:       int(du.Volumes.TotalCount),
			Active:      int(du.Volumes.ActiveCount),
			TotalBytes:  du.Volumes.TotalSize,
			Reclaimable: du.Volumes.Reclaimable,
		},
		BuildCache: DiskUsageCategory{
			Count:       int(du.BuildCache.TotalCount),
			Active:      int(du.BuildCache.ActiveCount),
			TotalBytes:  du.BuildCache.TotalSize,
			Reclaimable: du.BuildCache.Reclaimable,
		},
	}, nil
}

// VolumeUsage returns measured on-disk bytes per Docker volume name. Runs the
// daemon's `system df` filesystem walk, so it is sweep-only — never per read.
func (e *engineClient) VolumeUsage(ctx context.Context) ([]VolumeUsage, error) {
	du, err := e.cli.DiskUsage(ctx, client.DiskUsageOptions{Volumes: true})
	if err != nil {
		return nil, err
	}
	return volumeUsageFrom(du.Volumes.Items), nil
}

// volumeUsageFrom projects the SDK volumes into our shape, skipping ones the daemon did not size
// (nil UsageData, or Size -1).
func volumeUsageFrom(vols []volume.Volume) []VolumeUsage {
	out := make([]VolumeUsage, 0, len(vols))
	for _, v := range vols {
		if v.UsageData == nil || v.UsageData.Size < 0 {
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
	pairs := []string{"dangling", "false"}
	if opts.Dangling {
		pairs = []string{"dangling", "true"}
	}
	if opts.Until != "" {
		pairs = append(pairs, "until", opts.Until)
	}
	rep, err := e.cli.ImagePrune(ctx, client.ImagePruneOptions{Filters: selectorFilters(pairs...)})
	if err != nil {
		return PruneReport{}, err
	}
	out := PruneReport{SpaceReclaimed: int64(rep.Report.SpaceReclaimed)}
	for _, d := range rep.Report.ImagesDeleted {
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
	rep, err := e.cli.BuildCachePrune(ctx, client.BuildCachePruneOptions{})
	if err != nil {
		return PruneReport{}, err
	}
	return PruneReport{
		ItemsDeleted:   rep.Report.CachesDeleted,
		SpaceReclaimed: int64(rep.Report.SpaceReclaimed),
	}, nil
}
