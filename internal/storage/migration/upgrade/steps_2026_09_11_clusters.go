// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package upgrade

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/slug"
	"gorm.io/gorm"
)

type clusterRow struct {
	ID              uint `gorm:"primaryKey"`
	Name            string
	DisplayName     string
	Mode            string
	IsDefault       bool
	ManagerServerID uint
	AgentTokenHash  string
	IngressServerID uint
	Visibility      string
	LegacyIngress   bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (clusterRow) TableName() string { return "clusters" }

type clusterServerRow struct {
	ID           uint
	Name         string
	DisplayName  string
	IsLocal      bool
	Connectivity string
	SwarmNodeID  string
	ClusterID    uint
}

func (clusterServerRow) TableName() string { return "servers" }

var clusterSettingKeys = []string{"cluster_name", "cluster_agent_token_hash"}

// clustersStep introduces clusters on an existing install (plans/multi-cluster-plan.md §10). The default
// cluster takes the control-plane host and its swarm members; every other node becomes its own standalone
// cluster, so gateways, DNS targets and Swarm objects stay exactly as they were. Safe to re-run.
func clustersStep(ctx context.Context, db *gorm.DB) error {
	if !db.Migrator().HasTable(&clusterRow{}) || !db.Migrator().HasColumn(&clusterServerRow{}, "cluster_id") {
		return nil
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		def, err := ensureDefaultCluster(tx)
		if err != nil {
			return fmt.Errorf("create the default cluster: %w", err)
		}
		if err := tx.Exec(`UPDATE servers SET cluster_id = ? WHERE cluster_id = 0 AND (is_local = ? OR swarm_node_id <> '')`,
			def.ID, true).Error; err != nil {
			return fmt.Errorf("assign default cluster members: %w", err)
		}
		var rest []clusterServerRow
		if err := tx.Where("cluster_id = 0").Order("id").Find(&rest).Error; err != nil {
			return err
		}
		for _, srv := range rest {
			if err := createStandaloneCluster(tx, srv); err != nil {
				return fmt.Errorf("create a standalone cluster for node %s: %w", srv.Name, err)
			}
		}
		for _, table := range []string{"applications", "database_instances", "volumes", "jobs"} {
			err := tx.Exec(`UPDATE `+table+` SET cluster_id = COALESCE(
				(SELECT s.cluster_id FROM servers s WHERE s.id = `+table+`.server_id AND s.cluster_id <> 0), ?)
				WHERE cluster_id = 0`, def.ID).Error
			if err != nil {
				return fmt.Errorf("backfill %s.cluster_id: %w", table, err)
			}
		}
		if err := backfillStackClusters(tx, def.ID); err != nil {
			return err
		}
		return adoptClusterSettings(tx, def)
	})
}

func ensureDefaultCluster(tx *gorm.DB) (*clusterRow, error) {
	var def clusterRow
	err := tx.Where("is_default = ?", true).First(&def).Error
	if err == nil {
		return &def, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var members int64
	if err := tx.Model(&clusterServerRow{}).Where("is_local = ? AND swarm_node_id <> ''", true).Count(&members).Error; err != nil {
		return nil, err
	}
	name, err := slug.Unique("default", "default", clusterNameTaken(tx))
	if err != nil {
		return nil, err
	}
	def = clusterRow{Name: name, Mode: "standalone", IsDefault: true, Visibility: "all"}
	if members > 0 {
		def.Mode = "swarm"
	}
	return &def, tx.Create(&def).Error
}

func createStandaloneCluster(tx *gorm.DB, srv clusterServerRow) error {
	name, err := slug.Unique(srv.Name, "node", clusterNameTaken(tx))
	if err != nil {
		return err
	}
	label := strings.TrimSpace(srv.DisplayName)
	if label == "" {
		label = srv.Name
	}
	c := clusterRow{
		Name:            name,
		DisplayName:     label,
		Mode:            "standalone",
		ManagerServerID: srv.ID,
		Visibility:      "all",
		LegacyIngress:   srv.Connectivity == "port-forward",
	}
	if srv.Connectivity == "edge-gateway" {
		c.IngressServerID = srv.ID
	}
	if err := tx.Create(&c).Error; err != nil {
		return err
	}
	return tx.Model(&clusterServerRow{}).Where("id = ?", srv.ID).Update("cluster_id", c.ID).Error
}

// backfillStackClusters puts a stack in its members' cluster. A stack whose members already span
// clusters cannot live in one, so it goes to the default cluster and is logged for the admin.
func backfillStackClusters(tx *gorm.DB, defaultID uint) error {
	var stacks []struct {
		ID   uint
		Name string
	}
	if err := tx.Table("stacks").Select("id, name").Where("cluster_id = 0").Find(&stacks).Error; err != nil {
		return err
	}
	for _, st := range stacks {
		var ids []uint
		if err := tx.Table("applications").Where("stack_id = ? AND cluster_id <> 0", st.ID).
			Distinct().Pluck("cluster_id", &ids).Error; err != nil {
			return err
		}
		target := defaultID
		switch {
		case len(ids) == 1:
			target = ids[0]
		case len(ids) > 1:
			logger.Warn("stack members span several clusters; assigning the stack to the default cluster",
				"stack", st.Name, "clusters", ids)
		}
		if err := tx.Table("stacks").Where("id = ?", st.ID).Update("cluster_id", target).Error; err != nil {
			return err
		}
	}
	return nil
}

func adoptClusterSettings(tx *gorm.DB, def *clusterRow) error {
	var rows []struct {
		Key   string
		Value string
	}
	if err := tx.Table("settings").Select("key, value").Where("key IN ?", clusterSettingKeys).Find(&rows).Error; err != nil {
		return err
	}
	cols := map[string]any{}
	for _, row := range rows {
		value := strings.TrimSpace(row.Value)
		switch {
		case row.Key == "cluster_name" && def.DisplayName == "" && value != "":
			cols["display_name"] = value
		case row.Key == "cluster_agent_token_hash" && def.AgentTokenHash == "" && value != "":
			cols["agent_token_hash"] = value
		}
	}
	if len(cols) > 0 {
		if err := tx.Model(&clusterRow{}).Where("id = ?", def.ID).Updates(cols).Error; err != nil {
			return err
		}
	}
	return tx.Exec(`DELETE FROM settings WHERE key IN ?`, clusterSettingKeys).Error
}

func clusterNameTaken(tx *gorm.DB) func(string) (bool, error) {
	return func(name string) (bool, error) {
		var n int64
		err := tx.Model(&clusterRow{}).Where("name = ?", name).Count(&n).Error
		return n > 0, err
	}
}

func init() {
	steps = append(steps, Step{
		Name:    "clusters_introduce",
		Version: "1.10.0",
		Run:     clustersStep,
	})
}
