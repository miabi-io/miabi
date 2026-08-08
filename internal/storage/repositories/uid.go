// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import "gorm.io/gorm"

// idByUID returns the numeric primary key of the row in T's table with the given uid. It lets
// handlers accept either a numeric id or a portable uid in a route param; the caller's
// workspace-scoped lookup then enforces ownership, so resolving a uid never leaks across tenants.
func idByUID[T any](db *gorm.DB, uid string) (uint, error) {
	var id uint
	if err := db.Model(new(T)).Where("uid = ?", uid).Limit(1).Pluck("id", &id).Error; err != nil {
		return 0, err
	}
	if id == 0 {
		return 0, gorm.ErrRecordNotFound
	}
	return id, nil
}
