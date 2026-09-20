// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package housekeeping

import (
	"strings"

	"github.com/miabi-io/miabi/internal/docker"
)

// The two guarded reclaim categories. Unlike the dangling-image and build-cache prunes, which hand
// the whole category to the daemon, these are resolved to an explicit list here and then removed one
// by one, so the guard below is the only thing that decides what goes and the dry-run is exact.

// ImageRefSource yields image references that must survive an unused-image reclaim. Satisfied by the
// application, release, database and node repositories.
type ImageRefSource interface {
	ImageRefs() ([]string, error)
}

// SetImageRefs wires the referenced-image guard. Without it the unused-image category stays empty:
// an unguarded prune would take the image a stopped app or a rollback needs.
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

// unusedImages resolves the images that can be reclaimed: no container holds them, they are not
// dangling (that category prunes separately), a record does not name them, and they carry a repo
// digest, so removing one is undone by a pull. A referenced set of nil means the guard is not wired
// and nothing is reclaimable.
func unusedImages(imgs []docker.Image, referenced map[string]bool) []docker.Image {
	if referenced == nil {
		return nil
	}
	out := make([]docker.Image, 0, len(imgs))
	for _, im := range imgs {
		if im.Dangling || im.Containers != 0 || len(im.RepoTags) == 0 {
			continue
		}
		// Never remove what cannot be pulled back: a locally built image that was never pushed has
		// no repo digest, and no record has to name it for its loss to be unrecoverable.
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

// unsharedBytes is what removing an image actually frees: its own layers, minus the ones other
// images keep alive. Summing Size instead counts a shared base layer once per image that carries it,
// which is how an estimate ends up promising several times the disk the apply can return.
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

// unusedVolumes resolves the volumes that can be reclaimed: no container holds them and nothing
// about them is Miabi's. A volume the platform owns is never in this category however idle it looks
// — a stopped app's data volume holds no container either — and reaches the report, if at all,
// through the precise orphan path instead. claimed reports whether a volume row names the volume.
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

// isMiabiName reports whether a Docker name follows one of the platform's own naming conventions.
// Volumes created before they were labelled are recognised by nothing else.
func isMiabiName(name string) bool {
	return strings.HasPrefix(name, "mb-") || strings.HasPrefix(name, "miabi_") || strings.HasPrefix(name, "miabi-")
}
