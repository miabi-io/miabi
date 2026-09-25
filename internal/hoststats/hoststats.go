// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package hoststats re-exports the procfs readers in pkg/hoststats for existing callers and adds the
// helper-container sampler, which is control-plane only.
package hoststats

import (
	"context"

	"github.com/miabi-io/miabi/pkg/hoststats"
)

// Stats is a point-in-time host resource snapshot.
type Stats = hoststats.Stats

// Available reports whether procPath exposes a readable /proc/stat.
func Available(procPath string) bool { return hoststats.Available(procPath) }

// Read samples host CPU and memory from procPath.
func Read(ctx context.Context, procPath string) (Stats, error) {
	return hoststats.Read(ctx, procPath)
}
