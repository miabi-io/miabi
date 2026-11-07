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
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

type advancedRouteRow struct {
	ID             uint
	WorkspaceID    uint
	Name           string
	Hosts          string // JSON array, as the model serializes it
	Path           string
	Enabled        bool
	AdvancedConfig string
}

// Affected advanced routes are disabled, not rewritten: lifting unchecked hosts would bless the hijack (C1).
func routeAdvancedHostsStep(ctx context.Context, db *gorm.DB) error {
	if !db.Migrator().HasTable("routes") || !db.Migrator().HasColumn("routes", "advanced_config") {
		return nil
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []advancedRouteRow
		if err := tx.Table("routes").
			Where("advanced_config IS NOT NULL AND advanced_config <> ?", "").
			Find(&rows).Error; err != nil {
			return err
		}
		for _, r := range rows {
			claimed := advancedClaimsRouting(r.AdvancedConfig)
			hostless := !advancedRowHasHost(r.Hosts)
			if !claimed && !hostless {
				continue
			}

			reason := "advanced config set hosts, path or priority"
			if !claimed {
				reason = "advanced route has no structured hosts"
			}
			if err := tx.Table("routes").Where("id = ?", r.ID).
				Update("enabled", false).Error; err != nil {
				return err
			}
			logger.Warn("route disabled by upgrade: advanced config requires review",
				"route_id", r.ID, "workspace_id", r.WorkspaceID, "route", r.Name, "reason", reason)
		}
		return nil
	})
}

// Unparseable YAML counts as claiming: it can't be cleared, and Goma would reject the bundle anyway.
func advancedClaimsRouting(cfg string) bool {
	if strings.TrimSpace(cfg) == "" {
		return false
	}
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(cfg), &raw); err != nil {
		return true
	}
	for key := range raw {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "hosts", "path", "priority", "name", "enabled", "disabled", "target", "destination", "backends":
			return true
		}
	}
	return false
}

// advancedRowHasHost reports whether the serialized hosts column holds a non-empty entry.
func advancedRowHasHost(hosts string) bool {
	h := strings.TrimSpace(hosts)
	if h == "" || h == "[]" || h == "null" {
		return false
	}
	var list []string
	if err := yaml.Unmarshal([]byte(h), &list); err != nil {
		return false
	}
	for _, v := range list {
		if strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

func init() {
	steps = append(steps, Step{
		Name:    "route_advanced_hosts_review",
		Version: "1.10.2",
		Run:     routeAdvancedHostsStep,
	})
}
