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
	"strings"

	"gorm.io/gorm"
)

var externalAccessSettingKeys = map[string]string{
	"external_base_domain":   "external_base_domain",
	"external_base_provider": "external_cert_provider",
}

// A field the cluster already has is kept, so the step is safe to re-run.
func externalAccessStep(ctx context.Context, db *gorm.DB) error {
	if !db.Migrator().HasTable("clusters") || !db.Migrator().HasColumn("clusters", "external_base_domain") {
		return nil
	}
	keys := make([]string, 0, len(externalAccessSettingKeys))
	for key := range externalAccessSettingKeys {
		keys = append(keys, key)
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []struct {
			Key   string
			Value string
		}
		if err := tx.Table("settings").Select("key, value").Where("key IN ?", keys).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			value := strings.TrimSpace(row.Value)
			if value == "" {
				continue
			}
			if row.Key == "external_base_domain" {
				value = strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(value, "*."), "."))
			}
			def, err := ensureDefaultCluster(tx)
			if err != nil {
				return err
			}
			col := externalAccessSettingKeys[row.Key]
			err = tx.Table("clusters").Where("id = ? AND ("+col+" IS NULL OR "+col+" = '')", def.ID).Update(col, value).Error
			if err != nil {
				return err
			}
		}
		return tx.Exec(`DELETE FROM settings WHERE key IN ?`, keys).Error
	})
}

func init() {
	steps = append(steps, Step{
		Name:    "external_access_per_cluster",
		Version: "1.10.0",
		Run:     externalAccessStep,
	})
}
