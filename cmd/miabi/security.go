// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"

	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/worker"
)

// newSecurityResolver hardens app/job containers (RestrictedUID:0, no-new-privileges, NET_RAW
// dropped) for every workspace the quota service says must drop root. An app or job may swap the
// UID for its own non-root one; the worker applies that on top. Nil without a configured UID.
func newSecurityResolver(cfg *config.Config, q *quota.Service) worker.SecurityResolver {
	if cfg.RestrictedUID <= 0 {
		return nil
	}
	user := fmt.Sprintf("%d:0", cfg.RestrictedUID) // GID 0: arbitrary-UID convention
	return worker.SecurityFunc(func(workspaceID uint, officialTemplate bool) worker.Security {
		if !q.RequireNonRootUser(workspaceID, officialTemplate) {
			return worker.Security{} // profile is "default": image user, no hardening
		}
		return worker.Security{User: user, NoNewPrivileges: true, CapDrop: []string{"NET_RAW"}, Restricted: true}
	})
}
