// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import "testing"

func TestReservedSettingsAreHiddenFromTheGenericList(t *testing.T) {
	reserved := []string{
		"app.install_id",
		"image.postgres",
		"brand.name",
		"brand.links",
		"brand.accent",
		"brand.logo_url",
		"cluster_name",
	}
	for _, key := range reserved {
		if !isReservedSetting(key) {
			t.Errorf("%q is editable in the generic settings list, but has its own screen", key)
		}
	}

	editable := []string{
		"maintenance_mode",
		"default_workspace_role",
		"custom_labels_enabled",
		"repo_pipelines_enabled",
		"require_email_verification",
		"allowed_signup_domains",
		"external_base_domain",
		"branding_notes",
		"cluster_names",
		"imagemagick_enabled",
	}
	for _, key := range editable {
		if isReservedSetting(key) {
			t.Errorf("%q was reserved, but it belongs in the settings list", key)
		}
	}
}
