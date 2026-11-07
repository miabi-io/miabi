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
	"encoding/json"
	"strings"

	"github.com/jkaninda/logger"
	"gorm.io/gorm"
)

type certRow struct {
	ID          uint
	WorkspaceID uint
	Name        string
	CommonName  string
	DNSNames    string // JSON array, as the model serializes it
}

type certDomainRow struct {
	WorkspaceID uint
	Name        string
	Verified    bool
	Banned      bool
}

// Goma's global cert map lets an exact match beat ACME, so certs for unverified names are detached from routes.
func certVerifiedDomainsStep(ctx context.Context, db *gorm.DB) error {
	if !db.Migrator().HasTable("certificates") || !db.Migrator().HasTable("domains") {
		return nil
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var certs []certRow
		if err := tx.Table("certificates").Find(&certs).Error; err != nil {
			return err
		}
		if len(certs) == 0 {
			return nil
		}
		var domains []certDomainRow
		if err := tx.Table("domains").Find(&domains).Error; err != nil {
			return err
		}
		byWorkspace := map[uint][]certDomainRow{}
		for _, d := range domains {
			byWorkspace[d.WorkspaceID] = append(byWorkspace[d.WorkspaceID], d)
		}

		for _, c := range certs {
			bad := firstUncoveredName(c, byWorkspace[c.WorkspaceID])
			if bad == "" {
				continue
			}
			res := tx.Table("routes").Where("certificate_id = ?", c.ID).
				Updates(map[string]any{"certificate_id": nil, "tls_mode": "acme"})
			if res.Error != nil {
				return res.Error
			}
			logger.Warn("certificate detached by upgrade: its domain is not verified",
				"certificate_id", c.ID, "workspace_id", c.WorkspaceID, "certificate", c.Name,
				"name", bad, "routes_detached", res.RowsAffected)
		}
		return nil
	})
}

func firstUncoveredName(c certRow, domains []certDomainRow) string {
	names := []string{c.CommonName}
	var sans []string
	if strings.TrimSpace(c.DNSNames) != "" {
		if err := json.Unmarshal([]byte(c.DNSNames), &sans); err == nil {
			names = append(names, sans...)
		}
	}
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" {
			continue
		}
		if !nameCoveredByVerified(n, domains) {
			return n
		}
	}
	return ""
}

func nameCoveredByVerified(name string, domains []certDomainRow) bool {
	for _, d := range domains {
		dn := strings.ToLower(strings.TrimSpace(d.Name))
		if dn == "" || !d.Verified || d.Banned {
			continue
		}
		if name == dn || strings.HasSuffix(name, "."+dn) {
			return true
		}
	}
	return false
}

func init() {
	steps = append(steps, Step{
		Name:    "certificate_verified_domains",
		Version: "1.10.2",
		Run:     certVerifiedDomainsStep,
	})
}
