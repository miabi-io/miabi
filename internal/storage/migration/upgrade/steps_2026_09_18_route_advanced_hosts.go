// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// routeAdvancedHostsStep closes the hostname hijack (C1) on routes that already exist.
//
// An advanced route used to carry its hosts and path inside the raw YAML, where nothing checked them
// against the workspace's verified domains or against hostnames another tenant already served. A
// route could therefore claim any hostname on the platform — including the console's, by declaring a
// longer path on the platform domain and outranking it.
//
// Validation now refuses those keys, but stored configs predate it. Every affected route is DISABLED
// rather than rewritten: its hosts were never checked against any domain the workspace owns, so
// lifting them into the structured field would bless exactly the claim this fixes. An admin re-enters
// the hostname, which sends it through the normal gate.
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

// advancedClaimsRouting reports whether a stored config sets a field that decides which requests the
// route receives. Unparseable YAML counts as claiming: it cannot be cleared, and Goma would reject
// the whole bundle over it anyway.
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
