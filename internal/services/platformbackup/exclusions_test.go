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

package platformbackup

import (
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

// Self-backup grows unbounded, tenant volumes weren't asked for, and mid-push registry blobs fail on pull.
func TestExcludedVolume(t *testing.T) {
	workspaceLabels := map[string]string{docker.LabelWorkspace: "7"}
	registryLabels := docker.PlatformLabels(docker.RoleRegistry, docker.ManagedByMiabi, nil)

	cases := []struct {
		name   string
		volume string
		labels map[string]string
		want   bool
		why    string
	}{
		{"platform backup volume", legacyLocalVolume, nil, true, "must never back itself up"},
		{"workspace backup volume", "mb-backups-3", nil, true, "must never back itself up"},
		{"registry data by name", models.DefaultRegistryVolume, nil, true, "live blob storage"},
		{"registry data by label", models.DefaultRegistryVolume, registryLabels, true, "live blob storage"},
		{"tenant volume", "mb-ws7-uploads", workspaceLabels, true, "tenant data, not platform state"},
		{"tenant volume without mb prefix", "customer-data", workspaceLabels, true, "tenant data, not platform state"},
		{"gateway providers", "mb-node-gateway-providers", nil, false, "genuine platform state"},
		{"unlabelled platform volume", "mb-something", nil, false, "genuine platform state"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := excludedVolume(tc.volume, tc.labels); got != tc.want {
				t.Errorf("excludedVolume(%q) = %v, want %v (%s)", tc.volume, got, tc.want, tc.why)
			}
		})
	}
}
