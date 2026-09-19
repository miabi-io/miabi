// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package upgrade

import (
	"context"

	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

type orphanWorkloadRow struct {
	ID          uint
	WorkspaceID uint
	Name        string
	ServerID    uint
	Status      string
}

// orphanedNodeWorkloadsStep marks applications and database instances whose node was deleted as
// stopped.
func orphanedNodeWorkloadsStep(ctx context.Context, db *gorm.DB) error {
	if !db.Migrator().HasTable("servers") {
		return nil
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, table := range []string{"database_instances", "applications"} {
			if !db.Migrator().HasTable(table) {
				continue
			}
			var rows []orphanWorkloadRow
			// server_id 0 is the control-plane node, which is never deleted.
			if err := tx.Table(table).
				Where("server_id <> ? AND status <> ?", 0, "stopped").
				Where("server_id NOT IN (?)", tx.Table("servers").Select("id")).
				Find(&rows).Error; err != nil {
				return err
			}
			for _, r := range rows {
				if err := tx.Table(table).Where("id = ?", r.ID).
					Update("status", "stopped").Error; err != nil {
					return err
				}
				logger.Warn("workload marked stopped by upgrade: its node no longer exists",
					"table", table, "id", r.ID, "name", r.Name, "workspace_id", r.WorkspaceID,
					"server_id", r.ServerID, "was", r.Status)
			}
		}
		return nil
	})
}

func init() {
	steps = append(steps, Step{
		Name:    "orphaned_node_workloads",
		Version: "1.10.2",
		Run:     orphanedNodeWorkloadsStep,
	})
}
