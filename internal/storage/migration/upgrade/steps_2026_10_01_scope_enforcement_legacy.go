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

	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Scopes were never enforced before, so existing keys may rely on access they were never granted.
func scopeEnforcementLegacyStep(ctx context.Context, db *gorm.DB) error {
	var keys int64
	if err := db.WithContext(ctx).Table("api_keys").Where("revoked = ? AND ephemeral = ?", false, false).Count(&keys).Error; err != nil {
		return fmt.Errorf("count api keys: %w", err)
	}
	if keys == 0 {
		return nil
	}
	return db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
		Create(&models.Setting{Key: middlewares.LegacyScopeModeSetting, Value: "warn", Type: models.SettingTypeString}).Error
}

func init() {
	steps = append(steps, Step{
		Name:    "api_key_scope_enforcement_legacy",
		Version: "1.10.11",
		Run:     scopeEnforcementLegacyStep,
	})
}
