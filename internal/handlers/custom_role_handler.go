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

package handlers

import (
	"errors"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/customrole"
	"github.com/miabi-io/miabi/internal/services/workspace"
	"gorm.io/gorm"
)

// Writes are gated on custom_roles; enforcement of already-assigned roles is open-source.
type CustomRoleHandler struct {
	svc        *customrole.Service
	workspaces *workspace.Service
	ee         enterprise.EE
	audit      *audit.Logger
}

func NewCustomRoleHandler(svc *customrole.Service, workspaces *workspace.Service, ee enterprise.EE, auditLog *audit.Logger) *CustomRoleHandler {
	return &CustomRoleHandler{svc: svc, workspaces: workspaces, ee: ee, audit: auditLog}
}

type CustomRoleRequest struct {
	Body struct {
		Name        string   `json:"name" required:"true"`
		BaseRole    string   `json:"base_role" required:"true" enum:"owner,admin,developer,viewer"`
		Permissions []string `json:"permissions" required:"true"`
	} `json:"body"`
}

type AssignCustomRoleRequest struct {
	Body struct {
		CustomRoleID uint `json:"custom_role_id" required:"true"`
	} `json:"body"`
}

// List returns the workspace's custom roles (any member may read).
func (h *CustomRoleHandler) List(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	roles, err := h.svc.List(wsID)
	if err != nil {
		return c.AbortInternalServerError("failed to list roles", err)
	}
	return ok(c, roles)
}

// Create defines a custom role (gated custom_roles; no-escalation enforced).
func (h *CustomRoleHandler) Create(c *okapi.Context, req *CustomRoleRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagCustomRoles); err != nil {
		return entitlementAbort(c, err)
	}
	wsID := middlewares.WorkspaceID(c)
	role, err := h.svc.Create(wsID, h.input(req), middlewares.Permissions(c))
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "workspace.role.create", strconv.Itoa(int(role.ID)))
	return created(c, role)
}

// Update edits a custom role.
func (h *CustomRoleHandler) Update(c *okapi.Context, req *CustomRoleRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagCustomRoles); err != nil {
		return entitlementAbort(c, err)
	}
	wsID := middlewares.WorkspaceID(c)
	id, err := uintParam(c, "id")
	if err != nil {
		return c.AbortBadRequest("invalid id")
	}
	role, err := h.svc.Update(wsID, id, h.input(req), middlewares.Permissions(c))
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "workspace.role.update", strconv.Itoa(int(role.ID)))
	return ok(c, role)
}

// Delete removes a custom role (409 if assigned).
func (h *CustomRoleHandler) Delete(c *okapi.Context) error {
	if err := h.ee.RequireMutable(enterprise.FlagCustomRoles); err != nil {
		return entitlementAbort(c, err)
	}
	wsID := middlewares.WorkspaceID(c)
	id, err := uintParam(c, "id")
	if err != nil {
		return c.AbortBadRequest("invalid id")
	}
	if err := h.svc.Delete(wsID, id); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "workspace.role.delete", strconv.Itoa(int(id)))
	return message(c, "role deleted")
}

// The assigner may not grant beyond their own rank, and the last owner may not lose owner rank.
func (h *CustomRoleHandler) AssignMember(c *okapi.Context, req *AssignCustomRoleRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagCustomRoles); err != nil {
		return entitlementAbort(c, err)
	}
	wsID := middlewares.WorkspaceID(c)
	memberID, err := uintParam(c, "userID")
	if err != nil {
		return c.AbortBadRequest("invalid user id")
	}
	role, err := h.svc.Get(wsID, req.Body.CustomRoleID)
	if err != nil {
		return h.mapErr(c, err)
	}
	// No-escalation: the assigner must hold every permission they're granting.
	actor := middlewares.Permissions(c)
	for p := range role.PermissionSet() {
		if !actor[p] {
			return c.AbortForbidden(customrole.ErrEscalation.Error(), customrole.ErrEscalation)
		}
	}
	if err := h.workspaces.AssignCustomRole(wsID, middlewares.WorkspaceRole(c), memberID, role.ID, role.BaseRole); err != nil {
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			return c.AbortNotFound("member not found")
		case errors.Is(err, workspace.ErrOutranked), errors.Is(err, workspace.ErrRoleAboveSelf),
			errors.Is(err, workspace.ErrWorkspaceLimitReached):
			return c.AbortForbidden(err.Error(), err)
		case errors.Is(err, workspace.ErrLastOwner):
			return c.AbortWithError(409, err)
		case errors.Is(err, workspace.ErrInvalidRole), errors.Is(err, workspace.ErrServiceAccountOwner):
			return c.AbortBadRequest(err.Error())
		default:
			return c.AbortInternalServerError("failed to assign role", err)
		}
	}
	h.record(c, wsID, "workspace.member_role_assign", strconv.Itoa(int(memberID)))
	return message(c, "role assigned")
}

func (h *CustomRoleHandler) input(req *CustomRoleRequest) customrole.Input {
	perms := make([]models.Permission, 0, len(req.Body.Permissions))
	for _, p := range req.Body.Permissions {
		perms = append(perms, models.Permission(p))
	}
	return customrole.Input{
		Name:        req.Body.Name,
		BaseRole:    models.WorkspaceRole(req.Body.BaseRole),
		Permissions: perms,
	}
}

func (h *CustomRoleHandler) mapErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, customrole.ErrEscalation):
		return c.AbortForbidden(err.Error(), err)
	case errors.Is(err, customrole.ErrRoleNotFound):
		return c.AbortNotFound(err.Error())
	case errors.Is(err, customrole.ErrRoleInUse):
		return c.AbortWithError(409, err)
	case errors.Is(err, customrole.ErrNameRequired),
		errors.Is(err, customrole.ErrInvalidBaseRole),
		errors.Is(err, customrole.ErrInvalidPermission),
		errors.Is(err, customrole.ErrNoPermissions):
		return c.AbortBadRequest(err.Error())
	default:
		return c.AbortInternalServerError("role operation failed", err)
	}
}

func (h *CustomRoleHandler) record(c *okapi.Context, wsID uint, action, target string) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID: &actor, WorkspaceID: &wsID, Action: action, TargetType: "custom_role",
		TargetID: target, IP: c.RealIP(),
	})
}
