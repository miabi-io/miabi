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
	"github.com/miabi-io/miabi/internal/services/database"
	"gorm.io/gorm"
)

// Only a DATABASE_URL still pointing at the database's own URL secret is copied; hand-written values are left.
func dbURLEnvStep(ctx context.Context, db *gorm.DB) error {
	tx := db.WithContext(ctx)
	var dbs []models.Database
	if err := tx.Where("application_id IS NOT NULL").Find(&dbs).Error; err != nil {
		return err
	}
	for i := range dbs {
		d := &dbs[i]
		var inst models.DatabaseInstance
		if err := tx.Select("id", "name", "engine").First(&inst, d.InstanceID).Error; err != nil {
			continue
		}
		legacy, url := "DATABASE_URL", "DB_URL"
		if d.EnvPrefix != "" {
			legacy, url = d.EnvPrefix+"_"+legacy, d.EnvPrefix+"_"+url
		}
		ref := "${{ secrets." + database.URLSecretName(&inst, d) + " }}"
		var src models.AppEnvVar
		if err := tx.Where("application_id = ? AND key = ? AND value = ?", *d.ApplicationID, legacy, ref).
			First(&src).Error; err != nil {
			continue
		}
		var n int64
		if err := tx.Model(&models.AppEnvVar{}).Where("application_id = ? AND key = ?", *d.ApplicationID, url).
			Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		if err := tx.Create(&models.AppEnvVar{ApplicationID: *d.ApplicationID, Key: url, Value: ref}).Error; err != nil {
			return err
		}
	}
	return nil
}

func init() {
	steps = append(steps, Step{
		Name:    "db_url_env_alias",
		Version: "1.10.13",
		Run:     dbURLEnvStep,
	})
}
