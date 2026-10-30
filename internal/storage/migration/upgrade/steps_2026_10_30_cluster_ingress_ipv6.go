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

	"gorm.io/gorm"
)

// clusterIngressIPv6Step moves an IPv6 public address out of ingress_ip, which now holds only IPv4, into the
// ingress_ipv6 column beside it. Safe to re-run.
func clusterIngressIPv6Step(ctx context.Context, db *gorm.DB) error {
	m := db.Migrator()
	if !m.HasTable("clusters") || !m.HasColumn("clusters", "ingress_ipv6") {
		return nil
	}
	return db.WithContext(ctx).Exec(`UPDATE clusters SET ingress_ipv6 = ingress_ip, ingress_ip = ''
		WHERE ingress_ip LIKE '%:%' AND (ingress_ipv6 IS NULL OR ingress_ipv6 = '')`).Error
}

func init() {
	steps = append(steps, Step{
		Name:    "cluster_ingress_ipv6",
		Version: "1.10.22",
		Run:     clusterIngressIPv6Step,
	})
}
