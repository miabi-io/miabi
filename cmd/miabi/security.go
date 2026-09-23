// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"

	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/worker"
)

// newSecurityResolver resolves the security profile for app/job containers: the baseline every
// workspace gets, plus the restricted profile (RestrictedUID:0, no-new-privileges) for workspaces
// the quota service says must drop root. An app or job may swap the UID for its own non-root one;
// the worker applies that on top.
func newSecurityResolver(cfg *config.Config, q *quota.Service) worker.SecurityResolver {
	user := ""
	if cfg.RestrictedUID > 0 {
		user = fmt.Sprintf("%d:0", cfg.RestrictedUID) // GID 0: arbitrary-UID convention
	}
	return worker.SecurityFunc(func(workspaceID uint, officialTemplate bool) worker.Security {
		if user == "" {
			return baselineSecurity()
		}

		restricted := q.RequireNonRootUser(workspaceID, false)
		return securityFor(user, restricted, restricted && !q.RequireNonRootUser(workspaceID, officialTemplate))
	})
}

func baselineSecurity() worker.Security {
	return worker.Security{CapDrop: []string{"NET_RAW"}}
}

func securityFor(user string, restricted, exemptUID bool) worker.Security {
	if !restricted {
		return baselineSecurity()
	}
	sec := worker.Security{NoNewPrivileges: true, CapDrop: []string{"NET_RAW"}, Restricted: true}
	if !exemptUID {
		sec.User = user
	}
	return sec
}
