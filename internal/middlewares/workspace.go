// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package middlewares

import (
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// WorkspaceScope resolves the workspace from the {workspace} path parameter, verifies the
// authenticated user is a member, and stores the workspace id and role in the context. The
// parameter resolves by shape: all-digits is an id, a uuid is a uid, otherwise the handle.
func WorkspaceScope(repo *repositories.WorkspaceRepository, customRoles *repositories.CustomRoleRepository) okapi.Middleware {
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
		return c.Next()
	}
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
			if id, err := strconv.Atoi(c.Param(paramName)); err == nil && id > 0 {
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

// WorkspaceRole returns the caller's role in the resolved workspace.
func WorkspaceRole(c *okapi.Context) models.WorkspaceRole {
	return models.WorkspaceRole(c.GetString(CtxWorkspaceRole))
}
