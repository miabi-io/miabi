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

package middlewares

import (
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
)

// MigrationPathPrefix is the tree another install pulls a workspace from; migration keys are refused
// anywhere else.
const MigrationPathPrefix = "/api/v1/migration/"

// RequireMigrationCaller admits only a migration key, never a session or another key, "*" included.
// Which workspace the key may export is checked by the handler: a workspace key names one, an
// instance key none.
func RequireMigrationCaller() okapi.Middleware {
	return func(c *okapi.Context) error {
		if AuthMethod(c) != "api_key" || c.GetString(CtxAPIKeyPurpose) != models.KeyPurposeMigration {
			return c.AbortForbidden("migrating a workspace needs a migration key")
		}
		if APIKeyAppID(c) != 0 || APIKeyEphemeral(c) {
			return c.AbortForbidden("a migration key must not be bound to an application")
		}
		return c.Next()
	}
}
