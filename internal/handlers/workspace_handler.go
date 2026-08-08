// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"strconv"
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/account"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/mailer"
	"github.com/miabi-io/miabi/internal/services/workspace"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

type WorkspaceHandler struct {
	svc     *workspace.Service
	account *account.Service
	audit   *repositories.AuditLogRepository
	users   *repositories.UserRepository
	log     *audit.Logger
	ee      enterprise.EE
	mailer  *mailer.Service
}

func NewWorkspaceHandler(svc *workspace.Service, acct *account.Service, auditRepo *repositories.AuditLogRepository, users *repositories.UserRepository, log *audit.Logger, ee enterprise.EE) *WorkspaceHandler {
	return &WorkspaceHandler{svc: svc, account: acct, audit: auditRepo, users: users, log: log, ee: ee}
}

// SetMailer wires the platform mailer used to email workspace invitations.
// Optional; without it (or without SMTP configured) the email is skipped.
func (h *WorkspaceHandler) SetMailer(m *mailer.Service) { h.mailer = m }

type CreateWorkspaceRequest struct {
	Body struct {
		// DisplayName is the free-text label; when omitted it defaults to the handle.
		DisplayName string `json:"display_name"`
		// Name is the desired unique handle (URL/docker name); auto-derived from the
		// display name when blank.
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"body"`
}

type UpdateWorkspaceRequest struct {
	Body struct {
		// DisplayName is the free-text label. The unique handle is changed only via
		// the dedicated rename endpoint.
		DisplayName string `json:"display_name"`
		Description string `json:"description"`
	} `json:"body"`
}

// UpdateWorkspaceNameRequest changes only the workspace name — its unique
// URL/CLI/docker handle.
type UpdateWorkspaceNameRequest struct {
	Body struct {
		Name string `json:"name" required:"true"`
	} `json:"body"`
}

type InviteRequest struct {
	Body struct {
		Email string `json:"email" required:"true" format:"email"`
		Role  string `json:"role" required:"true" enum:"owner,admin,developer,viewer"`
	} `json:"body"`
}

type UpdateMemberRoleRequest struct {
	Body struct {
		Role string `json:"role" required:"true" enum:"owner,admin,developer,viewer"`
	} `json:"body"`
}

type AcceptInviteRequest struct {
	Body struct {
		Token string `json:"token" required:"true"`
	} `json:"body"`
}

type InvitationCreated struct {
	ID    uint   `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
	Token string `json:"token"` // returned for delivery; not persisted in plaintext
}

// Create makes a new workspace owned by the caller.
func (h *WorkspaceHandler) Create(c *okapi.Context, req *CreateWorkspaceRequest) error {
	userID := middlewares.UserID(c)
	display := strings.TrimSpace(req.Body.DisplayName)
	handle := strings.TrimSpace(req.Body.Name)
	if display == "" {
		display = handle // no label given → default it to the handle
	}
	if display == "" && handle == "" {
		return c.AbortBadRequest("a workspace name is required")
	}
	ws, err := h.svc.Create(userID, display, handle, req.Body.Description)
	if err != nil {
		if errors.Is(err, workspace.ErrWorkspaceLimitReached) {
			return c.AbortWithError(403, err)
		}
		return c.AbortInternalServerError("failed to create workspace", err)
	}
	h.log.Record(audit.Entry{ActorID: &userID, WorkspaceID: &ws.ID, Action: "workspace.create", TargetType: "workspace", TargetID: strconv.Itoa(int(ws.ID)), IP: c.RealIP()})
	return created(c, ws)
}

// List returns workspaces the caller belongs to.
func (h *WorkspaceHandler) List(c *okapi.Context) error {
	workspaces, err := h.svc.ListForUser(middlewares.UserID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to list workspaces", err)
	}
	return ok(c, workspaces)
}

// Get returns a single workspace (membership enforced by WorkspaceScope).
func (h *WorkspaceHandler) Get(c *okapi.Context) error {
	ws, err := h.svc.Get(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortNotFound("workspace not found")
	}
	return ok(c, ws)
}

// Update edits workspace metadata — the display name and description (admin+).
// The unique handle is changed only via UpdateName.
func (h *WorkspaceHandler) Update(c *okapi.Context, req *UpdateWorkspaceRequest) error {
	ws, err := h.svc.Get(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortNotFound("workspace not found")
	}
	display := strings.TrimSpace(req.Body.DisplayName)
	// The built-in platform workspace's label is managed by the system; reject a
	// rename here (description may still be edited).
	if ws.System && display != "" && display != ws.DisplayName {
		return c.AbortWithError(409, errors.New("the system workspace cannot be renamed"))
	}
	if display != "" {
		ws.DisplayName = display
	}
	ws.Description = req.Body.Description
	if err := h.svc.Update(ws); err != nil {
		return c.AbortInternalServerError("failed to update workspace", err)
	}
	h.record(c, ws.ID, "workspace.update", "workspace", strconv.Itoa(int(ws.ID)))
	return ok(c, ws)
}

// UpdateName changes the workspace name — its unique URL/CLI/docker handle. The name is
// normalized and must stay unique and non-reserved; the system workspace's name is immutable.
// Audited admin+ action, since it changes URLs and the `docker login` handle.
func (h *WorkspaceHandler) UpdateName(c *okapi.Context, req *UpdateWorkspaceNameRequest) error {
	ws, err := h.svc.Get(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortNotFound("workspace not found")
	}
	newName := strings.TrimSpace(req.Body.Name)
	if err := h.svc.SetName(ws, newName); err != nil {
		switch {
		case errors.Is(err, workspace.ErrNameInvalid):
			return c.AbortBadRequest(err.Error())
		case errors.Is(err, workspace.ErrNameTaken), errors.Is(err, workspace.ErrNameReserved), errors.Is(err, workspace.ErrSystemNameLocked):
			return c.AbortWithError(409, err)
		default:
			return c.AbortInternalServerError("failed to update workspace name", err)
		}
	}
	if err := h.svc.Update(ws); err != nil {
		return c.AbortInternalServerError("failed to update workspace name", err)
	}
	h.record(c, ws.ID, "workspace.rename", "workspace", strconv.Itoa(int(ws.ID)))
	return ok(c, ws)
}

// Delete tears down a workspace and all its resources synchronously (owner
// only). The UI uses the streaming StartDeletion path for live progress; this
// REST endpoint gives CLI / API callers the same full teardown in one call.
func (h *WorkspaceHandler) Delete(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	if err := h.account.DeleteWorkspaceNow(c.Request().Context(), wsID); err != nil {
		if errors.Is(err, account.ErrSystemProtected) {
			return c.AbortWithError(409, err)
		}
		return c.AbortInternalServerError("failed to delete workspace", err)
	}
	h.record(c, wsID, "workspace.delete", "workspace", strconv.Itoa(int(wsID)))
	return message(c, "workspace deleted")
}

// StartDeletion begins an asynchronous workspace teardown and returns the
// initial job snapshot (owner only). The client streams live progress from
// DeletionJobEvents and navigates away once the job reports success.
func (h *WorkspaceHandler) StartDeletion(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	job, err := h.account.StartWorkspaceDeletion(wsID)
	if err != nil {
		if errors.Is(err, account.ErrSystemProtected) {
			return c.AbortWithError(409, err)
		}
		return c.AbortInternalServerError("failed to start workspace deletion", err)
	}
	h.record(c, wsID, "workspace.delete", "workspace", strconv.Itoa(int(wsID)))
	return ok(c, job)
}

// DeletionJob returns a one-shot snapshot of a deletion job (REST fallback).
func (h *WorkspaceHandler) DeletionJob(c *okapi.Context) error {
	snap, found := h.account.DeletionJobSnapshot(c.Param("jobID"))
	if !found {
		return c.AbortNotFound("deletion job not found")
	}
	return ok(c, snap)
}

// DeletionJobEvents streams a deletion job's live progress over SSE.
func (h *WorkspaceHandler) DeletionJobEvents(c *okapi.Context) error {
	found, err := h.account.StreamWorkspaceDeletion(c.Request().Context(), c.Param("jobID"), func(e eventbus.Event) error {
		return c.SSESendJSON(e)
	})
	if !found {
		return c.AbortNotFound("deletion job not found")
	}
	return err
}

// ListMembers returns the workspace members.
func (h *WorkspaceHandler) ListMembers(c *okapi.Context) error {
	members, err := h.svc.ListMembers(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to list members", err)
	}
	return ok(c, members)
}

func (h *WorkspaceHandler) UpdateMemberRole(c *okapi.Context, req *UpdateMemberRoleRequest) error {
	wsID := middlewares.WorkspaceID(c)
	memberUserID, err := strconv.Atoi(c.Param("userID"))
	if err != nil || memberUserID <= 0 {
		return c.AbortBadRequest("invalid user id")
	}
	if err := h.svc.UpdateMemberRole(wsID, middlewares.WorkspaceRole(c), uint(memberUserID), models.WorkspaceRole(req.Body.Role)); err != nil {
		return h.mapWorkspaceErr(c, err)
	}
	h.record(c, wsID, "workspace.member_role_update", "user", strconv.Itoa(memberUserID))
	return message(c, "member role updated")
}

// RemoveMember removes a member (admin+, bounded by the caller's rank).
func (h *WorkspaceHandler) RemoveMember(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	memberUserID, err := strconv.Atoi(c.Param("userID"))
	if err != nil || memberUserID <= 0 {
		return c.AbortBadRequest("invalid user id")
	}
	if err := h.svc.RemoveMember(wsID, middlewares.WorkspaceRole(c), uint(memberUserID)); err != nil {
		return h.mapWorkspaceErr(c, err)
	}
	h.record(c, wsID, "workspace.member_remove", "user", strconv.Itoa(memberUserID))
	return message(c, "member removed")
}

// Invite creates a pending invitation (admin+).
func (h *WorkspaceHandler) Invite(c *okapi.Context, req *InviteRequest) error {
	wsID := middlewares.WorkspaceID(c)
	userID := middlewares.UserID(c)
	raw, inv, err := h.svc.Invite(wsID, middlewares.WorkspaceRole(c), userID, req.Body.Email, models.WorkspaceRole(req.Body.Role))
	if err != nil {
		return h.mapWorkspaceErr(c, err)
	}
	h.record(c, wsID, "workspace.invite", "invitation", strconv.Itoa(int(inv.ID)))
	// Email the invitee an accept link (best-effort, async); the raw token is also
	// returned below so the UI can surface a copyable link.
	var wsName, inviterName string
	if ws, err := h.svc.Get(wsID); err == nil {
		wsName = ws.DisplayName
	}
	if inviter, err := h.users.FindByID(userID); err == nil {
		inviterName = inviter.Name
	}
	h.mailer.SendWorkspaceInvitation(inv.Email, wsName, inviterName, string(inv.Role), raw, inv.ExpiresAt)
	return created(c, InvitationCreated{ID: inv.ID, Email: inv.Email, Role: string(inv.Role), Token: raw})
}

// ListInvitations returns pending invitations (admin+).
func (h *WorkspaceHandler) ListInvitations(c *okapi.Context) error {
	invs, err := h.svc.ListInvitations(middlewares.WorkspaceID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to list invitations", err)
	}
	return ok(c, invs)
}

// AcceptInvite consumes an invitation token for the authenticated user.
func (h *WorkspaceHandler) AcceptInvite(c *okapi.Context, req *AcceptInviteRequest) error {
	userID := middlewares.UserID(c)
	ws, err := h.svc.Accept(req.Body.Token, userID)
	if err != nil {
		return h.mapWorkspaceErr(c, err)
	}
	h.log.Record(audit.Entry{ActorID: &userID, WorkspaceID: &ws.ID, Action: "workspace.invite_accept", TargetType: "workspace", TargetID: strconv.Itoa(int(ws.ID)), IP: c.RealIP()})
	return ok(c, ws)
}

// MyInvitations lists the pending invitations addressed to the caller's email,
// so an invited user can discover and accept them after signing in.
func (h *WorkspaceHandler) MyInvitations(c *okapi.Context) error {
	invs, err := h.svc.ListInvitationsForEmail(c.GetString("email"))
	if err != nil {
		return c.AbortInternalServerError("failed to list invitations", err)
	}
	return ok(c, invs)
}

// AcceptMyInvitation accepts a pending invitation by id, validating it is
// addressed to the caller's email (no raw token needed).
func (h *WorkspaceHandler) AcceptMyInvitation(c *okapi.Context) error {
	userID := middlewares.UserID(c)
	id, err := strconv.Atoi(c.Param("invitationID"))
	if err != nil || id <= 0 {
		return c.AbortBadRequest("invalid invitation id")
	}
	ws, err := h.svc.AcceptByID(uint(id), userID)
	if err != nil {
		return h.mapWorkspaceErr(c, err)
	}
	h.log.Record(audit.Entry{ActorID: &userID, WorkspaceID: &ws.ID, Action: "workspace.invite_accept", TargetType: "workspace", TargetID: strconv.Itoa(int(ws.ID)), IP: c.RealIP()})
	return ok(c, ws)
}

// AuditLog returns audit entries for the workspace, paginated (page/size).
func (h *WorkspaceHandler) AuditLog(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagAuditLog); err != nil {
		return entitlementAbort(c, err)
	}
	wsID := middlewares.WorkspaceID(c)
	page, size, offset := normalizePageParams(queryInt(c, "page", 0), queryInt(c, "size", 20))
	from, to := timeRange(c)
	entries, total, err := h.audit.ListByWorkspace(wsID, c.Query("order"), from, to, size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list audit log", err)
	}
	return paginated(c, entries, total, page, size)
}

// AuditLogDetail is a single audit entry enriched with the actor's name/email,
// which the AuditLog table stores only by id.
type AuditLogDetail struct {
	models.AuditLog
	ActorName  string `json:"actor_name,omitempty"`
	ActorEmail string `json:"actor_email,omitempty"`
}

// GetAuditLog returns a single audit entry (admin+), scoped to the workspace and
// enriched with the actor's name and email.
func (h *WorkspaceHandler) GetAuditLog(c *okapi.Context) error {
	if err := h.ee.Require(enterprise.FlagAuditLog); err != nil {
		return entitlementAbort(c, err)
	}
	wsID := middlewares.WorkspaceID(c)
	id, err := strconv.Atoi(c.Param("auditID"))
	if err != nil || id <= 0 {
		return c.AbortBadRequest("invalid audit id")
	}
	entry, err := h.audit.FindByID(uint(id))
	if err != nil {
		return c.AbortNotFound("audit entry not found")
	}
	// Don't leak entries from other workspaces.
	if entry.WorkspaceID == nil || *entry.WorkspaceID != wsID {
		return c.AbortNotFound("audit entry not found")
	}
	detail := AuditLogDetail{AuditLog: *entry}
	if entry.ActorID != nil {
		if u, err := h.users.FindByID(*entry.ActorID); err == nil {
			detail.ActorName, detail.ActorEmail = u.Name, u.Email
		}
	}
	return ok(c, detail)
}

func (h *WorkspaceHandler) record(c *okapi.Context, wsID uint, action, targetType, targetID string) {
	actor := middlewares.UserID(c)
	h.log.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action, TargetType: targetType, TargetID: targetID, IP: c.RealIP()})
}

func (h *WorkspaceHandler) mapWorkspaceErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, workspace.ErrLastOwner):
		return c.AbortWithError(409, err)
	case errors.Is(err, workspace.ErrWorkspaceLimitReached):
		return c.AbortWithError(403, err)
	case errors.Is(err, workspace.ErrAlreadyMember), errors.Is(err, workspace.ErrInvitePending):
		return c.AbortWithError(409, err)
	case errors.Is(err, workspace.ErrInvalidInvite):
		return c.AbortBadRequest("invalid or expired invitation")
	case errors.Is(err, workspace.ErrInvalidRole):
		return c.AbortBadRequest("invalid role")
	case errors.Is(err, workspace.ErrOutranked), errors.Is(err, workspace.ErrRoleAboveSelf):
		return c.AbortForbidden(err.Error(), err)
	default:
		return c.AbortInternalServerError("workspace operation failed", err)
	}
}
