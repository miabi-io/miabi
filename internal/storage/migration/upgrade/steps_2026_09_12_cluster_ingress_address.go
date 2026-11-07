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

	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

type ingressClusterRow struct {
	ID              uint
	Name            string
	IsDefault       bool
	ManagerServerID uint
	IngressServerID uint
	IngressIP       string
	IngressHostname string
}

// A hostname is taken only if it's a DNS name, not the machine hostname it was often auto-filled with.
func clusterIngressAddressStep(ctx context.Context, db *gorm.DB) error {
	if !db.Migrator().HasTable("clusters") || !db.Migrator().HasColumn("servers", "public_ip") {
		return nil
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var clusters []ingressClusterRow
		if err := tx.Table("clusters").Find(&clusters).Error; err != nil {
			return err
		}
		for _, c := range clusters {
			if c.IsDefault {
				res := tx.Table("servers").Where("cluster_id = ? AND is_local = ? AND connectivity = ?", c.ID, false, "edge-gateway").
					Update("connectivity", "cluster")
				if res.Error != nil {
					return res.Error
				}
				if res.RowsAffected > 0 {
					logger.Warn("nodes of the default cluster no longer run their own gateway; the control plane serves their apps",
						"nodes", res.RowsAffected)
				}
			}
			if strings.TrimSpace(c.IngressIP) != "" || strings.TrimSpace(c.IngressHostname) != "" {
				continue
			}
			if err := adoptNodeAddress(tx, c); err != nil {
				return err
			}
		}
		for _, col := range []string{"public_ip", "public_hostname"} {
			if tx.Migrator().HasColumn("servers", col) {
				// Raw DDL: SQLite's migrator can only drop a column of a parsed model, and the model no longer has these.
				if err := tx.Exec("ALTER TABLE servers DROP COLUMN " + col).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func adoptNodeAddress(tx *gorm.DB, c ingressClusterRow) error {
	q := tx.Table("servers").Select("public_ip, public_hostname")
	switch {
	case c.IsDefault:
		q = q.Where("is_local = ?", true)
	case c.IngressServerID != 0:
		q = q.Where("id = ?", c.IngressServerID)
	default:
		q = q.Where("id = ?", c.ManagerServerID)
	}
	var node struct {
		PublicIP       string
		PublicHostname string
	}
	if err := q.Limit(1).Scan(&node).Error; err != nil {
		return err
	}
	cols := map[string]any{}
	if ip := strings.TrimSpace(node.PublicIP); ip != "" {
		cols["ingress_ip"] = ip
	}
	if host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(node.PublicHostname), ".")); strings.Contains(host, ".") {
		cols["ingress_hostname"] = host
	}
	if len(cols) == 0 {
		return nil
	}
	return tx.Table("clusters").Where("id = ?", c.ID).Updates(cols).Error
}

func init() {
	steps = append(steps, Step{
		Name:    "cluster_ingress_address",
		Version: "1.10.0",
		Run:     clusterIngressAddressStep,
	})
}
