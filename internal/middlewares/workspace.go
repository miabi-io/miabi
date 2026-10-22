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
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/restriction"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// WorkspaceScope resolves the workspace from the {workspace} path parameter, verifies the
// authenticated user is a member, and stores the workspace id and role in the context. The
// parameter resolves by shape: all-digits is an id, a uuid is a uid, otherwise the handle. With a
// restriction gate, writes to a read-only or suspended workspace are refused (restrictionExempt aside).
func WorkspaceScope(repo *repositories.WorkspaceRepository, customRoles *repositories.CustomRoleRepository, gate *restriction.Service, apps ...AppRefResolver) okapi.Middleware {
	return func(c *okapi.Context) error {
		userID := UserID(c)
		if userID == 0 {
			return c.AbortUnauthorized("authentication required")
		}
		ref := strings.TrimSpace(c.Param("workspace"))
		wsID, err := strconv.Atoi(ref)
		if err != nil || wsID <= 0 {
			// Not a numeric id: resolve a uid (uuid-shaped) or, failing that, the
			// workspace handle (name).
			if _, uerr := uuid.Parse(ref); uerr == nil {
				if id, e := repo.IDByUID(ref); e == nil && id > 0 {
					wsID = int(id)
				}
			}
			if wsID <= 0 && ref != "" {
				if ws, e := repo.FindByName(ref); e == nil && ws.ID > 0 {
					wsID = int(ws.ID)
				}
			}
			if wsID <= 0 {
				return c.AbortBadRequest("invalid workspace")
			}
		}
		// A workspace-bound API key is a hard boundary: it may act only on the
		// workspace it is scoped to. (Account-wide keys carry no binding and fall
		// through to the membership check below.)
		if bound := APIKeyWorkspaceID(c); bound != nil && *bound != uint(wsID) {
			return c.AbortForbidden("this API key is scoped to a different workspace")
		}
		member, err := repo.FindMember(uint(wsID), userID)
		if err != nil {
			return c.AbortForbidden("you are not a member of this workspace")
		}
		// Resolve the effective rank + permission set. A member on a custom role
		// takes that role's permissions, with its BaseRole as the rank fallback so
		// the legacy rank-based RequireRole still resolves sanely.
		role := member.Role
		perms := models.RolePermissions(role)
		if member.CustomRoleID != nil && customRoles != nil {
			if cr, e := customRoles.FindByID(*member.CustomRoleID); e == nil {
				role = cr.BaseRole
				perms = cr.PermissionSet()
			}
		}
		c.Set(CtxWorkspaceID, wsID)
		c.Set(CtxWorkspaceRole, string(role))
		c.Set(CtxPermissions, perms)
		// An {appID} may be an id, a uid or the app's name: resolve it once, here, so every handler
		// and permission check sees the same numeric id.
		if ref := strings.TrimSpace(c.Param("appID")); ref != "" && len(apps) > 0 && apps[0] != nil {
			id, err := apps[0].IDByRef(uint(wsID), ref)
			if err != nil {
				return c.AbortNotFound("application not found")
			}
			c.Set(CtxAppID, int(id))
		}
		if !safeMethod(c.Request().Method) && !restrictionExempt(c.Request().Method, c.Request().URL.Path) {
			if err := gate.Allowed(uint(wsID), restriction.ActionWrite); err != nil {
				return c.AbortWithError(http.StatusForbidden, err)
			}
		}
		return c.Next()
	}
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

var workspaceRootPath = regexp.MustCompile(`^/api/v1/workspaces/[^/]+/?$`)

// restrictionExempt lists the writes a restricted workspace still accepts: anything that only
// reduces it (removing a member, stopping a workload, deleting the workspace) or takes data out.
func restrictionExempt(method, path string) bool {
	switch {
	case method == http.MethodDelete && strings.Contains(path, "/members/"):
		return true
	case method == http.MethodDelete && workspaceRootPath.MatchString(path):
		return true
	case method == http.MethodPost && (strings.HasSuffix(path, "/stop") || strings.HasSuffix(path, "/export") || strings.HasSuffix(path, "/leave")):
		return true
	}
	return false
}

// RequireRole enforces that the caller's workspace role is at least min.
// Must run after WorkspaceScope.
func RequireRole(min models.WorkspaceRole) okapi.Middleware {
	return func(c *okapi.Context) error {
		role := models.WorkspaceRole(c.GetString(CtxWorkspaceRole))
		if !role.AtLeast(min) {
			return c.AbortForbidden("insufficient permissions")
		}
		return c.Next()
	}
}

// RequirePermission enforces that the caller holds a specific permission. Must
// run after WorkspaceScope, which resolves the effective permission set (built-in
// role or custom role) into the context.
func RequirePermission(p models.Permission) okapi.Middleware {
	return func(c *okapi.Context) error {
		if !Permissions(c)[p] {
			return c.AbortForbidden("insufficient permissions")
		}
		return c.Next()
	}
}

// RequireResourcePermission enforces a permission held either at the workspace level or granted on
// the specific resource by a per-resource policy. Must run after WorkspaceScope; paramName is the
// path parameter carrying the resource id. With no policies stored this is RequirePermission.
func RequireResourcePermission(p models.Permission, resourceType, paramName string, policies *repositories.ResourcePolicyRepository) okapi.Middleware {
	return func(c *okapi.Context) error {
		if Permissions(c)[p] {
			return c.Next()
		}
		if policies != nil {
			id, err := strconv.Atoi(c.Param(paramName))
			if paramName == "appID" {
				if resolved := AppID(c); resolved != 0 {
					id, err = int(resolved), nil
				}
			}
			if err == nil && id > 0 {
				if policies.HasPermission(WorkspaceID(c), UserID(c), resourceType, uint(id), p) {
					return c.Next()
				}
			}
		}
		return c.AbortForbidden("insufficient permissions")
	}
}

// Permissions returns the caller's effective permission set resolved by
// WorkspaceScope. Falls back to the built-in role's set when the explicit set is
// absent (defensive — e.g. a route without WorkspaceScope).
func Permissions(c *okapi.Context) map[models.Permission]bool {
	if v, ok := c.Get(CtxPermissions); ok {
		if perms, ok := v.(map[models.Permission]bool); ok {
			return perms
		}
	}
	return models.RolePermissions(models.WorkspaceRole(c.GetString(CtxWorkspaceRole)))
}

// WorkspaceID returns the resolved workspace id (0 if absent).
func WorkspaceID(c *okapi.Context) uint {
	return uint(c.GetInt(CtxWorkspaceID))
}

// AppID returns the app resolved from the {appID} path parameter by WorkspaceScope, or 0.
func AppID(c *okapi.Context) uint {
	return uint(c.GetInt(CtxAppID))
}

// WorkspaceRole returns the caller's role in the resolved workspace.
func WorkspaceRole(c *okapi.Context) models.WorkspaceRole {
	return models.WorkspaceRole(c.GetString(CtxWorkspaceRole))
}
