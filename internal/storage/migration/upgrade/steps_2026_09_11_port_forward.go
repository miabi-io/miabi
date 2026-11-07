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

	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

type portBindingRow struct {
	ID uint `gorm:"primaryKey"`
}

func (portBindingRow) TableName() string { return "port_bindings" }

// Other port-forward nodes become edge gateways, flagged so the console can confirm or join them to a swarm.
func portForwardStep(ctx context.Context, db *gorm.DB) error {
	if !db.Migrator().HasTable(&clusterRow{}) || !db.Migrator().HasTable(&clusterServerRow{}) {
		return nil
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var servers []clusterServerRow
		if err := tx.Where("connectivity = ?", "port-forward").Find(&servers).Error; err != nil {
			return err
		}
		for _, srv := range servers {
			var mode string
			if err := tx.Model(&clusterRow{}).Select("mode").Where("id = ?", srv.ClusterID).Scan(&mode).Error; err != nil {
				return err
			}
			to := "edge-gateway"
			if srv.IsLocal || mode == "swarm" {
				to = "cluster"
			}
			if err := tx.Model(&clusterServerRow{}).Where("id = ?", srv.ID).Update("connectivity", to).Error; err != nil {
				return err
			}
			if to == "cluster" {
				continue
			}
			if err := tx.Model(&clusterRow{}).Where("id = ? AND is_default = ?", srv.ClusterID, false).
				Updates(map[string]any{"legacy_ingress": true, "ingress_server_id": srv.ID}).Error; err != nil {
				return err
			}
			logger.Warn("converted a port-forward node to an edge gateway: it needs public ports 80 and 443, "+
				"or join it to a swarm cluster from its cluster page", "node", srv.Name)
		}
		if !tx.Migrator().HasTable(&portBindingRow{}) {
			return nil
		}
		if tx.Migrator().HasColumn(&portBindingRow{}, "managed") {
			if err := tx.Where("managed = ?", true).Delete(&portBindingRow{}).Error; err != nil {
				return err
			}
			if err := tx.Migrator().DropColumn(&portBindingRow{}, "managed"); err != nil {
				return err
			}
		}
		if tx.Migrator().HasColumn(&portBindingRow{}, "bind_ip") {
			return tx.Migrator().DropColumn(&portBindingRow{}, "bind_ip")
		}
		return nil
	})
}

func init() {
	steps = append(steps, Step{
		Name:    "port_forward_retire",
		Version: "1.10.0",
		Run:     portForwardStep,
	})
}
