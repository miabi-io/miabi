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

// Written by 1.10.11 to keep upgraded installs in warn mode; MIABI_API_KEY_SCOPE_ENFORCEMENT is now the only opt-out.
const legacyScopeModeSetting = "api_key_scope_enforcement_legacy"

func scopeEnforcementOnStep(ctx context.Context, db *gorm.DB) error {
	if !db.Migrator().HasTable("settings") {
		return nil
	}
	return db.WithContext(ctx).Exec(`DELETE FROM settings WHERE key = ?`, legacyScopeModeSetting).Error
}

func init() {
	steps = append(steps, Step{
		Name:    "api_key_scope_enforcement_on",
		Version: "1.10.31",
		Run:     scopeEnforcementOnStep,
	})
}
