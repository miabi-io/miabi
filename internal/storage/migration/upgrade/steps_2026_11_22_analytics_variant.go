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

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

const analyticsRollupIndex = "idx_arollup"

// AutoMigrate adds the variant column but never changes an existing index's columns, so the key is rebuilt here.
func analyticsVariantKeyStep(ctx context.Context, db *gorm.DB) error {
	m := db.WithContext(ctx).Migrator()
	if !m.HasTable(&models.AnalyticsRollup{}) || !m.HasColumn(&models.AnalyticsRollup{}, "variant") {
		return nil
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		tm := tx.Migrator()
		if tm.HasIndex(&models.AnalyticsRollup{}, analyticsRollupIndex) {
			if err := tm.DropIndex(&models.AnalyticsRollup{}, analyticsRollupIndex); err != nil {
				return err
			}
		}
		return tm.CreateIndex(&models.AnalyticsRollup{}, analyticsRollupIndex)
	})
}

func init() {
	steps = append(steps, Step{
		Name:    "analytics_rollup_variant_key",
		Version: "1.10.31",
		Run:     analyticsVariantKeyStep,
	})
}
