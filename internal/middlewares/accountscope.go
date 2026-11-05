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

// workspaceKeyRefusal keeps a workspace-bound API key inside its workspace: nil when the request
// stays in it, else the refusal for the caller to abort with.
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
	// The migration tree admits only migration keys, which are workspace-bound by design; its
	// handlers check the binding against the workspace each call names.
	if strings.HasPrefix(path, MigrationPathPrefix) {
		return true
	}
	return method == http.MethodGet && accountReadable[reducePath(path)]
}

// reducePath reduces a request path to the suffix the allow-list is written in, so the list does not
// have to repeat the API version prefix.
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

// RequireSession refuses anything but a browser/JWT session. It guards the endpoints that change how
// an account is authenticated — minting API keys, enrolling or disabling 2FA, revoking sessions.
// Those are exactly the actions a stolen token would use to make itself permanent, and there is no
// legitimate automation reason to perform them with an API key rather than a login.
func RequireSession() okapi.Middleware {
	return func(c *okapi.Context) error {
		if AuthMethod(c) != "jwt" {
			return c.AbortForbidden("this endpoint requires a signed-in session, not an API key")
		}
		return c.Next()
	}
}
