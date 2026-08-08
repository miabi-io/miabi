// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"

	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/worker"
)

// newSecurityResolver hardens app/job containers (RestrictedUID:0, no-new-privileges,
// NET_RAW dropped) when ForceNonRootUser is set or the plan selects the restricted profile.
// Official templates keep the image user via AllowOfficialImageUser; nil without a UID.
func newSecurityResolver(cfg *config.Config, q *quota.Service) worker.SecurityResolver {
	if cfg.RestrictedUID <= 0 {
		return nil
	}
	user := fmt.Sprintf("%d:0", cfg.RestrictedUID) // GID 0: arbitrary-UID convention
	return worker.SecurityFunc(func(workspaceID uint, officialTemplate bool) worker.Security {
		if !cfg.ForceNonRootUser && !q.RestrictedProfile(workspaceID) {
			return worker.Security{} // profile is "default": image user, no hardening
		}
		// Restricted applies. Official-template apps may keep the image user when the
		// plan allows it — but never against a platform-wide non-root mandate.
		if !cfg.ForceNonRootUser && officialTemplate && q.AllowOfficialImageUser(workspaceID) {
			return worker.Security{}
		}
		return worker.Security{User: user, NoNewPrivileges: true, CapDrop: []string{"NET_RAW"}}
	})
}
