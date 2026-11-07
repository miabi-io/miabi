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
	"strings"

	"github.com/miabi-io/miabi/internal/docker"
)

// Resolved to an explicit list and removed one by one, so the guard alone decides and the dry run is exact.

type ImageRefSource interface {
	ImageRefs() ([]string, error)
}

// Without it the unused-image category stays empty: an unguarded prune would take an image a rollback needs.
func (s *Service) SetImageRefs(src ...ImageRefSource) { s.imageRefs = src }

// referencedImages collects every image reference the platform's records still name.
func (s *Service) referencedImages() (map[string]bool, error) {
	if len(s.imageRefs) == 0 {
		return nil, nil
	}
	set := map[string]bool{}
	for _, src := range s.imageRefs {
		refs, err := src.ImageRefs()
		if err != nil {
			return nil, err
		}
		for _, r := range refs {
			if r = strings.TrimSpace(r); r != "" {
				set[r] = true
			}
		}
	}
	return set, nil
}

// Requires a repo digest so removal is undone by a pull; nil referenced means the guard isn't wired.
func unusedImages(imgs []docker.Image, referenced map[string]bool) []docker.Image {
	if referenced == nil {
		return nil
	}
	out := make([]docker.Image, 0, len(imgs))
	for _, im := range imgs {
		if im.Dangling || im.Containers != 0 || len(im.RepoTags) == 0 {
			continue
		}
		// A never-pushed local build has no repo digest, so removing it would be unrecoverable.
		if len(im.RepoDigests) == 0 {
			continue
		}
		if referenced[im.ID] || anyReferenced(referenced, im.RepoTags) || anyReferenced(referenced, im.RepoDigests) {
			continue
		}
		out = append(out, im)
	}
	return out
}

// Summing Size counts shared base layers once per image and overstates what the apply frees.
func unsharedBytes(im docker.Image) int64 {
	if im.SharedSize <= 0 {
		return im.Size
	}
	if n := im.Size - im.SharedSize; n > 0 {
		return n
	}
	return 0
}

func anyReferenced(referenced map[string]bool, refs []string) bool {
	for _, r := range refs {
		if referenced[r] {
			return true
		}
	}
	return false
}

// Platform-owned volumes never land here, since a stopped app's data volume also has no container.
func unusedVolumes(vols []docker.Volume, usage []docker.VolumeUsage, claimed func(string) (bool, error)) ([]docker.VolumeUsage, error) {
	if claimed == nil {
		return nil, nil
	}
	byName := make(map[string]docker.Volume, len(vols))
	for _, v := range vols {
		byName[v.Name] = v
	}
	out := make([]docker.VolumeUsage, 0, len(usage))
	for _, u := range usage {
		if u.RefCount != 0 {
			continue
		}
		v, ok := byName[u.DockerName]
		if !ok {
			continue // listed by df but gone from the volume list: leave it alone
		}
		if isManaged(v.Labels) || isPlatformInfra(v.Labels) || isMiabiName(v.Name) {
			continue
		}
		held, err := claimed(v.Name)
		if err != nil {
			return nil, err
		}
		if held {
			continue
		}
		out = append(out, u)
	}
	return out, nil
}

// Pre-label volumes are recognised by nothing else.
func isMiabiName(name string) bool {
	return strings.HasPrefix(name, "mb-") || strings.HasPrefix(name, "miabi_") || strings.HasPrefix(name, "miabi-")
}
