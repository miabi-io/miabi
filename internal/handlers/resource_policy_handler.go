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
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// Enforcement of existing grants lives in RequireResourcePermission and is open-source.
type ResourcePolicyHandler struct {
	repo       *repositories.ResourcePolicyRepository
	workspaces *repositories.WorkspaceRepository
	ee         enterprise.EE
	audit      *audit.Logger
}

func NewResourcePolicyHandler(repo *repositories.ResourcePolicyRepository, workspaces *repositories.WorkspaceRepository, ee enterprise.EE, auditLog *audit.Logger) *ResourcePolicyHandler {
	return &ResourcePolicyHandler{repo: repo, workspaces: workspaces, ee: ee, audit: auditLog}
}

type GrantPolicyRequest struct {
	Body struct {
		UserID      uint     `json:"user_id" required:"true"`
		Permissions []string `json:"permissions" required:"true"`
	} `json:"body"`
}

// ListApp returns the grants on an application.
func (h *ResourcePolicyHandler) ListApp(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagResourcePolicies); err != nil {
		return entitlementAbort(c, err)
	}
	wsID := middlewares.WorkspaceID(c)
	appID, err := appIDParam(c)
	if err != nil {
		return c.AbortBadRequest("invalid app id")
	}
	policies, err := h.repo.ListByResource(wsID, models.ResourceTypeApp, appID)
	if err != nil {
		return c.AbortInternalServerError("failed to list policies", err)
	}
	return ok(c, policies)
}

// No-escalation: the granting admin must hold every permission being granted.
func (h *ResourcePolicyHandler) GrantApp(c *okapi.Context, req *GrantPolicyRequest) error {
	if err := h.ee.RequireMutable(enterprise.FlagResourcePolicies); err != nil {
		return entitlementAbort(c, err)
	}
	wsID := middlewares.WorkspaceID(c)
	appID, err := appIDParam(c)
	if err != nil {
		return c.AbortBadRequest("invalid app id")
	}
	// The grantee must be a member of the workspace.
	if _, err := h.workspaces.FindMember(wsID, req.Body.UserID); err != nil {
		return c.AbortBadRequest("user is not a member of this workspace")
	}
	actor := middlewares.Permissions(c)
	perms := make([]string, 0, len(req.Body.Permissions))
	for _, raw := range req.Body.Permissions {
		p := models.Permission(raw)
		if !models.IsValidPermission(p) {
			return c.AbortBadRequest("unknown permission: " + raw)
		}
		if !actor[p] {
			return c.AbortForbidden("you cannot grant a permission you do not hold", nil)
		}
		perms = append(perms, raw)
	}
	if len(perms) == 0 {
		return c.AbortBadRequest("at least one permission is required")
	}
	policy := &models.ResourcePolicy{
		WorkspaceID: wsID, UserID: req.Body.UserID,
		ResourceType: models.ResourceTypeApp, ResourceID: appID, Permissions: perms,
	}
	if err := h.repo.Upsert(policy); err != nil {
		return c.AbortInternalServerError("failed to grant policy", err)
	}
	h.record(c, wsID, "workspace.policy.grant", appID, req.Body.UserID)
	return created(c, policy)
}

// RevokeApp removes a user's grant on an application.
func (h *ResourcePolicyHandler) RevokeApp(c *okapi.Context) error {
	if err := h.ee.RequireMutable(enterprise.FlagResourcePolicies); err != nil {
		return entitlementAbort(c, err)
	}
	wsID := middlewares.WorkspaceID(c)
	appID, err := appIDParam(c)
	if err != nil {
		return c.AbortBadRequest("invalid app id")
	}
	userID, err := uintParam(c, "userID")
	if err != nil {
		return c.AbortBadRequest("invalid user id")
	}
	if err := h.repo.DeleteForUser(wsID, userID, models.ResourceTypeApp, appID); err != nil {
		return c.AbortInternalServerError("failed to revoke policy", err)
	}
	h.record(c, wsID, "workspace.policy.revoke", appID, userID)
	return message(c, "policy revoked")
}

func (h *ResourcePolicyHandler) record(c *okapi.Context, wsID uint, action string, appID, userID uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID: &actor, WorkspaceID: &wsID, Action: action, TargetType: models.ResourceTypeApp,
		TargetID: strconv.Itoa(int(appID)), IP: c.RealIP(),
		Metadata: map[string]any{"user_id": userID},
	})
}
