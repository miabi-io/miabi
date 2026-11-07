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

package docker

import (
	"context"

	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

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

func isDanglingImage(repoTags []string) bool {
	for _, t := range repoTags {
		if t != "" && t != "<none>:<none>" {
			return false
		}
	}
	return true
}

func (e *engineClient) DiskUsage(ctx context.Context) (DiskUsage, error) {
	du, err := e.cli.DiskUsage(ctx, client.DiskUsageOptions{
		Containers: true, Images: true, Volumes: true, BuildCache: true, Verbose: true,
	})
	if err != nil {
		return DiskUsage{}, err
	}
	return DiskUsage{
		VolumeItems: volumeUsageFrom(du.Volumes.Items),
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

// Runs the daemon's system df walk, so it is sweep-only, never per read.
func (e *engineClient) VolumeUsage(ctx context.Context) ([]VolumeUsage, error) {
	// Without Verbose the daemon returns an empty item list that reads as "no volumes" rather than an error.
	du, err := e.cli.DiskUsage(ctx, client.DiskUsageOptions{Volumes: true, Verbose: true})
	if err != nil {
		return nil, err
	}
	return volumeUsageFrom(du.Volumes.Items), nil
}

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

// Without Dangling it removes every unreferenced image, so callers must apply their referenced-image guard first.
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

// BuildKit never touches records held by in-progress builds, so All only widens to every unused record.
func (e *engineClient) PruneBuildCache(ctx context.Context) (PruneReport, error) {
	rep, err := e.cli.BuildCachePrune(ctx, client.BuildCachePruneOptions{All: true})
	if err != nil {
		return PruneReport{}, err
	}
	return PruneReport{
		ItemsDeleted:   rep.Report.CachesDeleted,
		SpaceReclaimed: int64(rep.Report.SpaceReclaimed),
	}, nil
}
