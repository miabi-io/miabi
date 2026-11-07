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

package edgegateway

import (
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
)

// MissingConfigKey lists imported gateways that never get GOMA_CONFIG_ENCRYPTION_KEY and so can't read encrypted config.
func MissingConfigKey(servers []models.Server) []string {
	var out []string
	for _, s := range servers {
		if s.GatewayImported && !s.IsLocal {
			out = append(out, s.Name)
		}
	}
	return out
}

// Warns loudly: silently skipping encryption would let an operator believe the config is protected.
func WarnMissingConfigKey(key string, servers []models.Server) {
	if strings.TrimSpace(key) == "" {
		return
	}
	stranded := MissingConfigKey(servers)
	if len(stranded) == 0 {
		return
	}
	logger.Error("gateway config encryption is on, but these nodes run an imported gateway Miabi cannot "+
		"hand the key to — their routes will fail to load until GOMA_CONFIG_ENCRYPTION_KEY is set on those "+
		"gateways, or they are replaced by a Miabi-managed gateway",
		"nodes", strings.Join(stranded, ", "))
}
