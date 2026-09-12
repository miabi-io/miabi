// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// portForwardStep retires port-forward connectivity (plans/multi-cluster-plan.md §7). The control-plane node
// and swarm members are served by their cluster's gateway; any other port-forward node becomes an edge
// gateway, flagged on its cluster so the console offers to confirm that or join it to a swarm. Safe to re-run.
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
