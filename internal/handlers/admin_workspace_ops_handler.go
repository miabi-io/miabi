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
	"crypto/rand"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/account"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/restriction"
	"github.com/miabi-io/miabi/internal/services/workspace"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"golang.org/x/crypto/bcrypt"
)

// AdminWorkspaceOpsHandler lets a platform admin (or an operator API key) create workspaces for
// users, restrict them, stop and start their workloads, and delete them.
type AdminWorkspaceOpsHandler struct {
	workspaces   *repositories.WorkspaceRepository
	users        *repositories.UserRepository
	plans        *repositories.PlanRepository
	wsService    *workspace.Service
	account      *account.Service
	restrictions *restriction.Service
	audit        *audit.Logger
}

func NewAdminWorkspaceOpsHandler(workspaces *repositories.WorkspaceRepository, users *repositories.UserRepository, plans *repositories.PlanRepository,
	wsService *workspace.Service, acct *account.Service, restrictions *restriction.Service, auditLog *audit.Logger) *AdminWorkspaceOpsHandler {
	return &AdminWorkspaceOpsHandler{workspaces: workspaces, users: users, plans: plans, wsService: wsService, account: acct,
		restrictions: restrictions, audit: auditLog}
}

// codedError is a refusal with a stable machine code for the error envelope.
type codedError struct {
	code string
	msg  string
}

func (e *codedError) Error() string { return e.msg }
func (e *codedError) Code() string  { return e.code }

// unusablePasswordHash is a bcrypt hash of random bytes nobody knows, for an account that signs in
// by SSO or sets its password through the reset flow.
func unusablePasswordHash() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	hash, _ := bcrypt.GenerateFromPassword(b, bcrypt.DefaultCost)
	return string(hash)
}

var errWorkspaceNameTaken = &codedError{code: "WORKSPACE_NAME_TAKEN", msg: "a workspace with this name already exists"}

// adminWorkspaceRef resolves an admin {id} path parameter: a numeric id, or the workspace's name.
func adminWorkspaceRef(repo *repositories.WorkspaceRepository, raw string) (*models.Workspace, error) {
	raw = strings.TrimSpace(raw)
	if id, err := strconv.Atoi(raw); err == nil && id > 0 {
		return repo.FindByID(uint(id))
	}
	return repo.FindByName(raw)
}

func (h *AdminWorkspaceOpsHandler) record(c *okapi.Context, wsID uint, action string, meta map[string]any) {
	actor := middlewares.UserID(c)
	if meta == nil {
		meta = map[string]any{}
	}
	if why := strings.TrimSpace(c.Header("X-Request-Reason")); why != "" {
		meta["request_reason"] = why
	}
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action, TargetType: "workspace",
		TargetID: strconv.Itoa(int(wsID)), IP: c.RealIP(), Metadata: meta})
}

// AdminCreateWorkspaceRequest creates a workspace for a user.
type AdminCreateWorkspaceRequest struct {
	Body struct {
		Name        string `json:"name" required:"true" doc:"Workspace handle, used exactly"`
		DisplayName string `json:"display_name"`
		OwnerEmail  string `json:"owner_email" required:"true" format:"email"`
		OwnerName   string `json:"owner_name"`
		PlanID      *uint  `json:"plan_id"`
	} `json:"body"`
}

// AdminCreateWorkspaceResponse is the new workspace and whether its owner account was created.
type AdminCreateWorkspaceResponse struct {
	Workspace    *models.Workspace `json:"workspace"`
	OwnerID      uint              `json:"owner_id"`
	OwnerCreated bool              `json:"owner_created"`
}

// Create makes a workspace owned by owner_email. A missing owner account is created without a
// password: the person signs in with SSO (linked by verified e-mail) or sets one with "forgot password".
func (h *AdminWorkspaceOpsHandler) Create(c *okapi.Context, req *AdminCreateWorkspaceRequest) error {
	if h.wsService.ConsoleOnly() {
		return c.AbortWithError(http.StatusForbidden, &workspace.CreationDeniedError{Mode: models.WorkspaceCreationConsole})
	}
	email := strings.ToLower(strings.TrimSpace(req.Body.OwnerEmail))
	if !strings.Contains(email, "@") {
		return c.AbortBadRequest("owner_email must be an e-mail address")
	}
	var plan *models.Plan
	if req.Body.PlanID != nil {
		p, err := h.plans.FindByID(*req.Body.PlanID)
		if err != nil {
			return c.AbortBadRequest("no such plan")
		}
		if p.System {
			return c.AbortWithError(http.StatusConflict, errSystemPlanAssign)
		}
		plan = p
	}
	// Checked before an owner account is created, so a refused name leaves no account behind.
	if taken, err := h.workspaces.ExistsByName(slug.Make(req.Body.Name, "")); err != nil {
		return c.AbortInternalServerError("failed to check the workspace name", err)
	} else if taken {
		return c.AbortWithError(http.StatusConflict, errWorkspaceNameTaken)
	}
	owner, err := h.users.FindByEmail(email)
	ownerCreated := false
	if err != nil {
		now := time.Now()
		owner = &models.User{
			Name: strings.TrimSpace(req.Body.OwnerName), Email: email, PasswordHash: unusablePasswordHash(),
			Role: models.SystemRoleUser, Active: true, EmailVerifiedAt: &now, SignupSource: models.SignupSourceAdmin,
		}
		if err := h.users.Create(owner); err != nil {
			return c.AbortInternalServerError("failed to create the owner account", err)
		}
		ownerCreated = true
	} else if !owner.Active || owner.IsService() {
		return c.AbortWithError(http.StatusConflict, &codedError{code: "OWNER_UNAVAILABLE", msg: "the owner account is disabled or is a service account"})
	}
	ws, err := h.wsService.CreateForOwner(owner.ID, strings.TrimSpace(req.Body.DisplayName), req.Body.Name)
	switch {
	case errors.Is(err, workspace.ErrNameTaken):
		return c.AbortWithError(http.StatusConflict, errWorkspaceNameTaken)
	case errors.Is(err, workspace.ErrNameInvalid), errors.Is(err, workspace.ErrNameReserved):
		return c.AbortBadRequest(err.Error())
	case err != nil:
		return c.AbortInternalServerError("failed to create workspace", err)
	}
	if plan != nil {
		ws.PlanID = &plan.ID
		if err := h.workspaces.Update(ws); err != nil {
			return c.AbortInternalServerError("workspace created but the plan could not be set", err)
		}
	}
	h.record(c, ws.ID, "admin.workspace.create", map[string]any{"name": ws.Name, "owner_email": email, "owner_created": ownerCreated, "plan_id": req.Body.PlanID})
	return created(c, AdminCreateWorkspaceResponse{Workspace: ws, OwnerID: owner.ID, OwnerCreated: ownerCreated})
}

// AdminWorkspaceRestrictionRequest restricts a workspace.
type AdminWorkspaceRestrictionRequest struct {
	Body restriction.SetInput `json:"body"`
}

// SetRestriction makes a workspace read-only or suspended.
func (h *AdminWorkspaceOpsHandler) SetRestriction(c *okapi.Context, req *AdminWorkspaceRestrictionRequest) error {
	ws, err := adminWorkspaceRef(h.workspaces, c.Param("id"))
	if err != nil {
		return c.AbortNotFound("workspace not found")
	}
	out, err := h.restrictions.Set(c.Request().Context(), ws.ID, middlewares.UserID(c), req.Body)
	switch {
	case errors.Is(err, restriction.ErrInvalidState), errors.Is(err, restriction.ErrUntilPast):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, restriction.ErrSystem):
		return c.AbortWithError(http.StatusConflict, err)
	case err != nil:
		return c.AbortInternalServerError("failed to restrict workspace", err)
	}
	h.record(c, ws.ID, "admin.workspace.restrict", map[string]any{"state": req.Body.State, "reason": req.Body.Reason, "until": req.Body.Until, "from": ws.Restriction})
	return ok(c, out)
}

// LiftRestriction removes a workspace's restriction, restarting what a suspension stopped.
func (h *AdminWorkspaceOpsHandler) LiftRestriction(c *okapi.Context) error {
	ws, err := adminWorkspaceRef(h.workspaces, c.Param("id"))
	if err != nil {
		return c.AbortNotFound("workspace not found")
	}
	out, err := h.restrictions.Lift(c.Request().Context(), ws.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to lift the restriction", err)
	}
	h.record(c, ws.ID, "admin.workspace.restriction_lift", map[string]any{"from": ws.Restriction})
	return ok(c, out)
}

// Stop stops every running app and database instance of a workspace.
func (h *AdminWorkspaceOpsHandler) Stop(c *okapi.Context) error {
	ws, err := adminWorkspaceRef(h.workspaces, c.Param("id"))
	if err != nil {
		return c.AbortNotFound("workspace not found")
	}
	if ws.System {
		return c.AbortWithError(http.StatusConflict, account.ErrSystemProtected)
	}
	res, _ := h.account.StopWorkspace(c.Request().Context(), ws.ID)
	h.record(c, ws.ID, "admin.workspace.stop", map[string]any{"apps": res.Apps, "databases": res.Databases})
	return ok(c, res)
}

// Start starts every stopped app and database instance of a workspace. A suspended workspace stays
// down: lift the suspension instead.
func (h *AdminWorkspaceOpsHandler) Start(c *okapi.Context) error {
	ws, err := adminWorkspaceRef(h.workspaces, c.Param("id"))
	if err != nil {
		return c.AbortNotFound("workspace not found")
	}
	if ws.Restriction == models.WorkspaceRestrictionSuspended {
		return c.AbortWithError(http.StatusConflict, &restriction.Error{State: ws.Restriction, Reason: ws.RestrictionReason})
	}
	res, err := h.account.StartWorkspace(c.Request().Context(), ws.ID, nil)
	if err != nil {
		return c.AbortInternalServerError("failed to start workspace", err)
	}
	h.record(c, ws.ID, "admin.workspace.start", map[string]any{"apps": res.Apps, "databases": res.Databases, "failed": res.Failed})
	return ok(c, res)
}

// Delete tears a workspace down at once, with every resource it holds.
func (h *AdminWorkspaceOpsHandler) Delete(c *okapi.Context) error {
	ws, err := adminWorkspaceRef(h.workspaces, c.Param("id"))
	if err != nil {
		return c.AbortNotFound("workspace not found")
	}
	if err := h.account.GuardDeletion(ws.ID); err != nil {
		return c.AbortWithError(http.StatusConflict, err)
	}
	if err := h.account.DeleteWorkspaceNow(c.Request().Context(), ws.ID); err != nil {
		if errors.Is(err, account.ErrSystemProtected) {
			return c.AbortWithError(http.StatusConflict, err)
		}
		return c.AbortInternalServerError("failed to delete workspace", err)
	}
	h.record(c, ws.ID, "admin.workspace.delete", map[string]any{"name": ws.Name})
	return message(c, "workspace deleted")
}
