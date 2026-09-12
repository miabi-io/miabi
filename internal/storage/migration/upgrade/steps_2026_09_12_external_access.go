// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// externalAccessStep moves one-click external access from platform settings onto the default cluster, now that
// every cluster keeps its own domain. Generated URLs keep their hosts. A field the cluster already has is kept, so
// the step is safe to re-run.
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
