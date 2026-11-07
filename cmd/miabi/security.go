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

package main

import (
	"fmt"

	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/worker"
)

// An app or job may swap the UID for its own non-root one; the worker applies that on top.
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
