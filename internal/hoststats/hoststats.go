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

// Package hoststats re-exports pkg/hoststats and adds the control-plane-only helper-container sampler.
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
