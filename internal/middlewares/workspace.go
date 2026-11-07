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
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/restriction"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// The param resolves by shape: all-digits is an id, a uuid is a uid, otherwise the handle.
func WorkspaceScope(repo *repositories.WorkspaceRepository, customRoles *repositories.CustomRoleRepository, gate *restriction.Service, apps ...AppRefResolver) okapi.Middleware {
	return func(c *okapi.Context) error {
		userID := UserID(c)
		if userID == 0 {
			return c.AbortUnauthorized("authentication required")
		}
		ref := strings.TrimSpace(c.Param("workspace"))
		wsID, err := strconv.Atoi(ref)
		if err != nil || wsID <= 0 {
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
		// A workspace-bound key is a hard boundary; account-wide keys fall through to the membership check.
		if bound := APIKeyWorkspaceID(c); bound != nil && *bound != uint(wsID) {
			return c.AbortForbidden("this API key is scoped to a different workspace")
		}
		member, err := repo.FindMember(uint(wsID), userID)
		if err != nil {
			return c.AbortForbidden("you are not a member of this workspace")
		}
		// BaseRole is the rank fallback so the legacy rank-based RequireRole still resolves.
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
		// Resolve once here so every handler and permission check sees the same numeric id.
		if ref := strings.TrimSpace(c.Param("appID")); ref != "" && len(apps) > 0 && apps[0] != nil {
			id, err := apps[0].IDByRef(uint(wsID), ref)
			if err != nil {
				return c.AbortNotFound("application not found")
			}
			c.Set(CtxAppID, int(id))
		}
		if !safeMethod(c.Request().Method) && !restrictionExempt(c.Request().Method, c.Request().URL.Path) {
			if err := gate.Allowed(uint(wsID), restriction.ActionWrite); err != nil {
				status := http.StatusForbidden
				var re *restriction.Error
				if errors.As(err, &re) && re.Billing {
					status = http.StatusPaymentRequired
				}
				return c.AbortWithError(status, err)
			}
		}
		return c.Next()
	}
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

var workspaceRootPath = regexp.MustCompile(`^/api/v1/workspaces/[^/]+/?$`)

// Exempt writes only reduce the workspace or take data out.
func restrictionExempt(method, path string) bool {
	switch {
	case method == http.MethodDelete && strings.Contains(path, "/members/"):
		return true
	case method == http.MethodDelete && workspaceRootPath.MatchString(path):
		return true
	case method == http.MethodPost && (strings.HasSuffix(path, "/stop") || strings.HasSuffix(path, "/export") || strings.HasSuffix(path, "/leave")):
		return true
	// A frozen workspace needs both to roll back or retry its move.
	case method == http.MethodPost && strings.HasSuffix(path, "/migration/unfreeze"):
		return true
	case strings.Contains(path, "/migration-keys"):
		return true
	}
	return false
}

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

// Must run after WorkspaceScope, which resolves the effective permission set.
func RequirePermission(p models.Permission) okapi.Middleware {
	return func(c *okapi.Context) error {
		if !Permissions(c)[p] {
			return c.AbortForbidden("insufficient permissions")
		}
		return c.Next()
	}
}

// Must run after WorkspaceScope; with no policies stored this is RequirePermission.
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

// Falls back to the built-in role's set, e.g. on a route without WorkspaceScope.
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
