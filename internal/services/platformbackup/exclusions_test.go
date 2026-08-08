// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package platformbackup

import (
	"testing"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

// Each exclusion here prevents a specific, silent failure. A backup volume that backs itself up grows
// without bound; a tenant volume in a *platform* backup is not what the operator asked for; and the
// registry's blob storage, archived mid-push, restores cleanly and then fails on pull.
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
