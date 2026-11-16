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

	"gorm.io/gorm"
)

// Volumes on a shared class predate shared classes being rwx, which left their services pinned to one node.
func sharedClassVolumesStep(ctx context.Context, db *gorm.DB) error {
	m := db.Migrator()
	if !m.HasTable("volumes") || !m.HasTable("storage_classes") || !m.HasColumn("storage_classes", "shared") {
		return nil
	}
	return db.WithContext(ctx).Exec(`UPDATE volumes SET access_mode = 'rwx'
		WHERE driver = 'local' AND access_mode = 'rwo' AND storage_class_name IN (
			SELECT name FROM storage_classes WHERE shared AND path <> '')`).Error
}

func init() {
	steps = append(steps, Step{
		Name:    "shared_class_volumes_rwx",
		Version: "1.10.29",
		Run:     sharedClassVolumesStep,
	})
}
