// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package drift

import (
	"context"
	"errors"

	"github.com/miabi-io/miabi/internal/docker"
)

// Replaced reports whether a volume that exists under its own name is no longer the one Miabi recorded:
// its engine creation timestamp moved, so it was deleted and created again, and it holds none of the data
// the record describes. A volume Miabi never recorded a timestamp for is not replaced — most predate the
// column, and calling them all replaced would cry wolf about every one of them.
func Replaced(recorded, engineCreatedAt string) bool {
	return recorded != "" && engineCreatedAt != "" && engineCreatedAt != recorded
}

// VolumeState reports whether a volume still holds the data its record describes: "" when it does,
// ClassMissing when it is gone, ClassReplaced when it was deleted and recreated. An engine that cannot
// answer returns an error and no class — not knowing is never evidence of loss.
func VolumeState(ctx context.Context, dc docker.Client, name, recordedCreatedAt string) (string, error) {
	v, err := dc.InspectVolume(ctx, name)
	switch {
	case errors.Is(err, docker.ErrNotFound):
		return ClassMissing, nil
	case err != nil:
		return "", err
	case Replaced(recordedCreatedAt, v.CreatedAt):
		return ClassReplaced, nil
	default:
		return "", nil
	}
}
