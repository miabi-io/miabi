// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// clusterIngressAddressStep moves the address public DNS records point at from nodes onto their clusters. A cluster
// without one takes its ingress node's, the control-plane node's for the default cluster, and a hostname only when
// it is a DNS name rather than the machine hostname it was often auto-filled with. A node of the default cluster
// stops running a gateway of its own, since the control plane serves every app there. Safe to re-run.
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
