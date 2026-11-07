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

package upgrade

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// An app couldn't change runtime cleanly before, so its current runtime is what its releases used.
func releaseRuntimeStep(ctx context.Context, db *gorm.DB) error {
	err := db.WithContext(ctx).Exec(`UPDATE releases SET runtime_kind = (
		SELECT COALESCE(NULLIF(a.runtime_kind, ''), 'container') FROM applications a WHERE a.id = releases.application_id)
		WHERE (runtime_kind IS NULL OR runtime_kind = '')
		  AND EXISTS (SELECT 1 FROM applications a WHERE a.id = releases.application_id)`).Error
	if err != nil {
		return fmt.Errorf("backfill release runtime: %w", err)
	}
	return nil
}

func init() {
	steps = append(steps, Step{
		Name:    "release_runtime",
		Version: "1.10.21",
		Run:     releaseRuntimeStep,
	})
}
