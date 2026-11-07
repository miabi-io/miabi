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
	"errors"
	"net/http"
	"strings"

	"github.com/jkaninda/okapi"
)

var accountReadable = map[string]bool{
	"/me":           true,
	"/permissions":  true,
	"/capabilities": true,
}

// Returns the refusal rather than aborting.
func workspaceKeyRefusal(c *okapi.Context) error {
	if workspaceKeyAllowed(c.Param("workspace"), c.Request().Method, c.Request().URL.Path) {
		return nil
	}
	return errors.New("this API key is scoped to a workspace and cannot be used on account-level endpoints")
}

func workspaceKeyAllowed(workspaceParam, method, path string) bool {
	if strings.TrimSpace(workspaceParam) != "" {
		return true
	}
	// Migration keys are workspace-bound; their handlers check the binding per call.
	if strings.HasPrefix(path, MigrationPathPrefix) {
		return true
	}
	return method == http.MethodGet && accountReadable[reducePath(path)]
}

func reducePath(path string) string {
	p := strings.TrimSuffix(path, "/")
	if i := strings.Index(p, "/api/v1"); i >= 0 {
		p = p[i+len("/api/v1"):]
	}
	if p == "" {
		return "/"
	}
	return p
}

// A stolen token would use these actions to make itself permanent; no automation needs them.
func RequireSession() okapi.Middleware {
	return func(c *okapi.Context) error {
		if AuthMethod(c) != "jwt" {
			return c.AbortForbidden("this endpoint requires a signed-in session, not an API key")
		}
		return c.Next()
	}
}
