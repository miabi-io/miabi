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
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/account"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/mailer"
	"github.com/miabi-io/miabi/internal/services/organization"
	"github.com/miabi-io/miabi/internal/services/session"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// AdminUserHandler manages platform users (super-admin only).
type AdminUserHandler struct {
	db         *gorm.DB
	users      *repositories.UserRepository
	sessions   *repositories.SessionRepository
	workspaces *repositories.WorkspaceRepository
	auditRepo  *repositories.AuditLogRepository
	store      *session.Store
	audit      *audit.Logger
	account    *account.Service
	mailer     *mailer.Service
	ee         enterprise.EE
	orgs       *organization.Service
	graceDays  int
	// guard refuses taking away a user the platform depends on, such as the locked billing service user.
	guard func(*models.User) error
}

// SetUserGuard wires the check run before an account is deactivated, suspended or deleted.
func (h *AdminUserHandler) SetUserGuard(fn func(*models.User) error) { h.guard = fn }

// guarded writes a 409 and reports true when the account may not be taken away.
func (h *AdminUserHandler) guarded(c *okapi.Context, target *models.User) (bool, error) {
	if h.guard == nil {
		return false, nil
	}
	if err := h.guard(target); err != nil {
		return true, c.AbortWithError(http.StatusConflict, err)
	}
	return false, nil
}

func (h *AdminUserHandler) SetMailer(m *mailer.Service) { h.mailer = m }

func (h *AdminUserHandler) SetEnterprise(ee enterprise.EE) { h.ee = ee }

func (h *AdminUserHandler) SetOrganizations(svc *organization.Service) { h.orgs = svc }

// AdminSetWorkspaceLimitRequest sets or clears a user's workspace-count override.
type AdminSetWorkspaceLimitRequest struct {
	Body struct {
		// null inherits max_workspaces_per_user; -1 = unlimited; 0 = none; N = at most N.
		Limit *int `json:"limit" min:"-1"`
	} `json:"body"`
}

func (h *AdminUserHandler) SetWorkspaceLimit(c *okapi.Context, req *AdminSetWorkspaceLimitRequest) error {
	if h.ee == nil || h.ee.RequireMutable(enterprise.FlagUserWorkspaceLimit) != nil {
		return entitlementAbort(c, h.requireLimitOverride())
	}
	target, err := h.targetUser(c)
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if err := h.users.SetWorkspaceLimit(target.ID, req.Body.Limit); err != nil {
		return c.AbortInternalServerError("failed to set workspace limit", err)
	}
	target.WorkspaceLimit = req.Body.Limit
	actor := middlewares.UserID(c)
	limit := "inherit"
	if req.Body.Limit != nil {
		limit = strconv.Itoa(*req.Body.Limit)
	}
	h.audit.Record(audit.Entry{
		ActorID: &actor, Action: "admin.user.workspace_limit.set", TargetType: "user",
		TargetID: strconv.Itoa(int(target.ID)), IP: c.RealIP(),
		Metadata: map[string]any{"limit": limit},
	})
	return ok(c, target)
}

// AdminSetWorkspaceCreationRequest sets or clears a user's workspace creation override.
type AdminSetWorkspaceCreationRequest struct {
	Body struct {
		// null clears the override (inherit).
		Allowed *bool `json:"allowed"`
	} `json:"body"`
}

func (h *AdminUserHandler) SetWorkspaceCreation(c *okapi.Context, req *AdminSetWorkspaceCreationRequest) error {
	if err := h.requireLimitOverride(); err != nil {
		return entitlementAbort(c, err)
	}
	target, err := h.targetUser(c)
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if target.IsService() {
		return c.AbortBadRequest("a service account cannot create workspaces")
	}
	if err := h.users.SetCanCreateWorkspaces(target.ID, req.Body.Allowed); err != nil {
		return c.AbortInternalServerError("failed to set workspace creation", err)
	}
	target.CanCreateWorkspaces = req.Body.Allowed
	value := "inherit"
	if req.Body.Allowed != nil {
		value = strconv.FormatBool(*req.Body.Allowed)
	}
	h.record(c, "admin.user.workspace_creation.set", target.ID, map[string]any{"allowed": value})
	return ok(c, target)
}

func (h *AdminUserHandler) requireLimitOverride() error {
	if h.ee == nil {
		return enterprise.ErrLicenseRequired
	}
	return h.ee.RequireMutable(enterprise.FlagUserWorkspaceLimit)
}

type AdminSetWorkspaceMembershipLimitRequest struct {
	Body struct {
		// null inherits max_workspace_memberships_per_user; -1 = unlimited; 0 = none; N = at most N.
		Limit *int `json:"limit" min:"-1"`
	} `json:"body"`
}

func (h *AdminUserHandler) SetWorkspaceMembershipLimit(c *okapi.Context, req *AdminSetWorkspaceMembershipLimitRequest) error {
	if h.ee == nil || h.ee.RequireMutable(enterprise.FlagUserWorkspaceMembershipLimit) != nil {
		return entitlementAbort(c, h.requireMembershipOverride())
	}
	target, err := h.targetUser(c)
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if err := h.users.SetWorkspaceMembershipLimit(target.ID, req.Body.Limit); err != nil {
		return c.AbortInternalServerError("failed to set membership limit", err)
	}
	target.WorkspaceMembershipLimit = req.Body.Limit
	actor := middlewares.UserID(c)
	limit := "inherit"
	if req.Body.Limit != nil {
		limit = strconv.Itoa(*req.Body.Limit)
	}
	h.audit.Record(audit.Entry{
		ActorID: &actor, Action: "admin.user.workspace_membership_limit.set", TargetType: "user",
		TargetID: strconv.Itoa(int(target.ID)), IP: c.RealIP(),
		Metadata: map[string]any{"limit": limit},
	})
	return ok(c, target)
}

func (h *AdminUserHandler) requireMembershipOverride() error {
	if h.ee == nil {
		return enterprise.ErrLicenseRequired
	}
	return h.ee.RequireMutable(enterprise.FlagUserWorkspaceMembershipLimit)
}

func NewAdminUserHandler(db *gorm.DB, users *repositories.UserRepository, sessions *repositories.SessionRepository, workspaces *repositories.WorkspaceRepository, auditRepo *repositories.AuditLogRepository, store *session.Store, auditLog *audit.Logger, acct *account.Service, graceDays int) *AdminUserHandler {
	if graceDays <= 0 {
		graceDays = 7
	}
	return &AdminUserHandler{db: db, users: users, sessions: sessions, workspaces: workspaces, auditRepo: auditRepo, store: store, audit: auditLog, account: acct, graceDays: graceDays}
}

type AdminCreateUserRequest struct {
	Body struct {
		Name string `json:"name" required:"true"`
		// Optional; auto-derived from the email local-part when omitted.
		Username string `json:"username"`
		Email    string `json:"email" required:"true" format:"email"`
		// Password is the user's temporary password. Omitted, one is generated and returned once.
		Password string `json:"password"`
		Role     string `json:"role" enum:"admin,user"`
		// Requires a configured system SMTP server.
		Notify bool `json:"notify"`
		// 0 or omitted leaves the user in the default organization.
		OrganizationID uint `json:"organization_id"`
	} `json:"body"`
}

type AdminUpdateUserRequest struct {
	Body struct {
		Role   string `json:"role" enum:"admin,user"`
		Active *bool  `json:"active"`
		// Username optionally changes the unique handle (an admin action).
		Username string `json:"username"`
	} `json:"body"`
}

// AdminListUsersRequest filters and pages the platform user list.
type AdminListUsersRequest struct {
	Search string `query:"search" description:"Matches name, username or email"`
	Kind   string `query:"kind" enum:"user,service" description:"user for people, service for service accounts; omit for both"`
	Page   int    `query:"page" default:"0"`
	Size   int    `query:"size" default:"20"`
}

// List returns a paginated, searchable list of users.
func (h *AdminUserHandler) List(c *okapi.Context, req *AdminListUsersRequest) error {
	page, size, offset := normalizePageParams(req.Page, req.Size)
	users, total, err := h.users.List(req.Search, req.Kind, size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list users", err)
	}
	return paginated(c, users, total, page, size)
}

type MemberSummary struct {
	UserID uint   `json:"user_id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

// Members lists the other members (ownership-transfer candidates); empty means delete-only.
type WorkspaceSummary struct {
	ID         uint            `json:"id"`
	Name       string          `json:"name"`
	Privileged bool            `json:"privileged"`
	Apps       int64           `json:"apps"`
	Databases  int64           `json:"databases"`
	Stacks     int64           `json:"stacks"`
	Members    []MemberSummary `json:"members"`
}

// AdminUserDetail is a user plus aggregate counts across the workspaces they own.
type AdminUserDetail struct {
	models.User
	WorkspacesOwned  int                `json:"workspaces_owned"`
	WorkspacesMember int64              `json:"workspaces_member"`
	AppsTotal        int64              `json:"apps_total"`
	AppsRunning      int64              `json:"apps_running"`
	AppsFailed       int64              `json:"apps_failed"`
	Databases        int64              `json:"databases"`
	Stacks           int64              `json:"stacks"`
	OwnedWorkspaces  []WorkspaceSummary `json:"owned_workspaces"`
	RecentEvents     []models.AuditLog  `json:"recent_events"`
	OrganizationName string             `json:"organization_name,omitempty"`
	Passkeys         int64              `json:"passkeys"`
}

// Get returns a single user with aggregate counts over the workspaces they own.
func (h *AdminUserHandler) Get(c *okapi.Context) error {
	user, err := h.targetUser(c)
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	owned, _ := h.workspaces.ListOwnedBy(user.ID)
	ownedIDs := make([]uint, 0, len(owned))
	for _, w := range owned {
		ownedIDs = append(ownedIDs, w.ID)
	}
	memberships, _ := h.workspaces.CountMemberships(user.ID)

	// Slices must marshal as [] not null: the web UI reads .length on them.
	detail := AdminUserDetail{
		User:             *user,
		WorkspacesOwned:  len(owned),
		WorkspacesMember: memberships,
		OwnedWorkspaces:  []WorkspaceSummary{},
		RecentEvents:     []models.AuditLog{},
	}
	detail.Passkeys, _ = repositories.NewPasskeyRepository(h.db).Count(user.ID)
	if h.orgs != nil {
		if org, oerr := h.orgs.Get(h.orgs.HomeOrganization(user)); oerr == nil {
			detail.OrganizationName = org.DisplayName
			if detail.OrganizationName == "" {
				detail.OrganizationName = org.Name
			}
		}
	}
	if len(ownedIDs) > 0 {
		detail.AppsTotal = h.countIn(&models.Application{}, ownedIDs, "")
		detail.AppsRunning = h.countIn(&models.Application{}, ownedIDs, string(models.AppStatusRunning))
		detail.AppsFailed = h.countIn(&models.Application{}, ownedIDs, string(models.AppStatusFailed))
		detail.Databases = h.countIn(&models.DatabaseInstance{}, ownedIDs, "")
		detail.Stacks = h.countIn(&models.Stack{}, ownedIDs, "")

		apps := groupCountByWorkspace(h.db, &models.Application{})
		dbs := groupCountByWorkspace(h.db, &models.DatabaseInstance{})
		stacks := groupCountByWorkspace(h.db, &models.Stack{})
		for _, w := range owned {
			summary := WorkspaceSummary{
				ID: w.ID, Name: w.DisplayName, Privileged: w.Privileged,
				Apps: apps[w.ID], Databases: dbs[w.ID], Stacks: stacks[w.ID],
				Members: []MemberSummary{},
			}
			// Other members are transfer candidates (the owner stays out of the list).
			if members, merr := h.workspaces.ListMembers(w.ID); merr == nil {
				for _, m := range members {
					if m.UserID == user.ID {
						continue
					}
					summary.Members = append(summary.Members, MemberSummary{
						UserID: m.UserID, Name: m.User.Name, Email: m.User.Email, Role: string(m.Role),
					})
				}
			}
			detail.OwnedWorkspaces = append(detail.OwnedWorkspaces, summary)
		}
	}
	if events, _ := h.auditRepo.ListByActor(user.ID, 20); len(events) > 0 {
		detail.RecentEvents = events
	}
	return ok(c, detail)
}

func (h *AdminUserHandler) countIn(model any, workspaceIDs []uint, status string) int64 {
	var n int64
	q := h.db.Model(model).Where("workspace_id IN ?", workspaceIDs)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	q.Count(&n)
	return n
}

// Create provisions a new user (admin-created accounts are email-verified).
func (h *AdminUserHandler) Create(c *okapi.Context, req *AdminCreateUserRequest) error {
	email := strings.ToLower(strings.TrimSpace(req.Body.Email))
	exists, err := h.users.ExistsByEmail(email)
	if err != nil {
		return c.AbortInternalServerError("failed to check email", err)
	}
	if exists {
		return c.AbortWithError(409, errEmailTaken)
	}
	// A blank handle is auto-derived by the User.BeforeCreate hook.
	username, err := validateUsername(h.users, req.Body.Username, 0)
	if err != nil {
		if errors.Is(err, errUsernameTaken) {
			return c.AbortWithError(409, err)
		}
		return c.AbortBadRequest(err.Error())
	}
	password, generated := req.Body.Password, req.Body.Password == ""
	if generated {
		if password, err = generatePassword(); err != nil {
			return c.AbortInternalServerError("failed to generate a password", err)
		}
	} else if err := models.CheckNewPassword(password); err != nil {
		return c.AbortBadRequest(err.Error())
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return c.AbortInternalServerError("failed to hash password", err)
	}
	role := models.SystemRoleUser
	if req.Body.Role == string(models.SystemRoleAdmin) {
		role = models.SystemRoleAdmin
	}
	var orgID *uint
	if req.Body.OrganizationID != 0 {
		if err := h.ee.RequireMutable(enterprise.FlagOrganizations); err != nil {
			return entitlementAbort(c, err)
		}
		if h.orgs == nil {
			return c.AbortBadRequest("organizations are not available")
		}
		if _, err := h.orgs.Get(req.Body.OrganizationID); err != nil {
			return c.AbortBadRequest("no such organization")
		}
		orgID = &req.Body.OrganizationID
	}
	now := time.Now()
	user := &models.User{
		Name:               strings.TrimSpace(req.Body.Name),
		Username:           username,
		Email:              email,
		PasswordHash:       string(hash),
		Role:               role,
		Active:             true,
		EmailVerifiedAt:    &now,
		OrganizationID:     orgID,
		SignupSource:       models.SignupSourceAdmin,
		MustChangePassword: true,
	}
	if err := h.users.Create(user); err != nil {
		return c.AbortInternalServerError("failed to create user", err)
	}
	h.record(c, "admin.user.create", user.ID, map[string]any{"email": user.Email, "role": string(role), "notified": req.Body.Notify, "organization_id": req.Body.OrganizationID})
	// Optionally email the new user a welcome (best-effort, async).
	if req.Body.Notify {
		h.mailer.SendWelcome(user.Email, user.Name)
	}
	resp := AdminCreateUserResponse{User: user}
	if generated {
		resp.Password = password
	}
	return created(c, resp)
}

// The generated password is returned only here: it is not stored in clear and cannot be read back.
type AdminCreateUserResponse struct {
	*models.User
	Password string `json:"password,omitempty"`
}

// Organization is immutable: owned workspaces would stay behind on clusters the new realm cannot see.
func (h *AdminUserHandler) Update(c *okapi.Context, req *AdminUpdateUserRequest) error {
	target, err := h.targetUser(c)
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	self := middlewares.UserID(c)
	wasActive := target.Active // captured before the active flag is mutated below

	if req.Body.Role != "" && req.Body.Role != string(target.Role) {
		if target.IsService() {
			return c.AbortBadRequest(errServiceAccountManaged.Error())
		}
		// Guard: an admin cannot demote themselves, and the last admin must remain.
		if target.ID == self {
			return c.AbortBadRequest("you cannot change your own role")
		}
		if target.Role == models.SystemRoleAdmin && req.Body.Role != string(models.SystemRoleAdmin) {
			if last, _ := h.isLastAdmin(target.ID); last {
				return c.AbortBadRequest("cannot demote the last remaining admin")
			}
		}
		target.Role = models.SystemRole(req.Body.Role)
	}

	if req.Body.Active != nil && *req.Body.Active != target.Active {
		if target.ID == self && !*req.Body.Active {
			return c.AbortBadRequest("you cannot deactivate your own account")
		}
		if !*req.Body.Active {
			if blocked, err := h.guarded(c, target); blocked {
				return err
			}
		}
		if !*req.Body.Active && target.Role == models.SystemRoleAdmin {
			if last, _ := h.isLastAdmin(target.ID); last {
				return c.AbortBadRequest("cannot deactivate the last remaining admin")
			}
		}
		target.Active = *req.Body.Active
	}

	if username, err := validateUsername(h.users, req.Body.Username, target.ID); err != nil {
		if errors.Is(err, errUsernameTaken) {
			return c.AbortWithError(409, err)
		}
		return c.AbortBadRequest(err.Error())
	} else if username != "" {
		target.Username = username
	}

	if err := h.users.Update(target); err != nil {
		return c.AbortInternalServerError("failed to update user", err)
	}
	// Best-effort: the account is already deactivated regardless of stop outcomes.
	if wasActive && !target.Active {
		h.revokeAll(c, target.ID)
	}
	if wasActive && !target.Active && h.account != nil {
		res := h.account.StopOwned(c.Request().Context(), target.ID)
		h.record(c, "admin.user.disable", target.ID, map[string]any{"apps_stopped": res.Apps, "databases_stopped": res.Databases})
	}
	h.record(c, "admin.user.update", target.ID, map[string]any{"role": string(target.Role), "active": target.Active})
	return ok(c, target)
}

type AdminScheduleDeletionRequest struct {
	Body struct {
		Transfers []struct {
			WorkspaceID uint `json:"workspace_id" required:"true"`
			NewOwnerID  uint `json:"new_owner_id" required:"true"`
		} `json:"transfers"`
	} `json:"body"`
}

// ScheduleDeletion marks a disabled account for deletion; transferred workspaces survive, the rest are purged.
func (h *AdminUserHandler) ScheduleDeletion(c *okapi.Context, req *AdminScheduleDeletionRequest) error {
	target, err := h.targetUser(c)
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if target.ID == middlewares.UserID(c) {
		return c.AbortBadRequest("you cannot delete your own account")
	}
	if blocked, err := h.guarded(c, target); blocked {
		return err
	}
	if target.Active {
		return c.AbortBadRequest("disable the account before scheduling deletion")
	}
	if target.Role == models.SystemRoleAdmin {
		if last, _ := h.isLastAdmin(target.ID); last {
			return c.AbortBadRequest("cannot delete the last remaining admin")
		}
	}

	// A transferred workspace is no longer owned by the user, so it won't be purged with the account.
	transferred := 0
	for _, t := range req.Body.Transfers {
		ws, err := h.workspaces.FindByID(t.WorkspaceID)
		if err != nil || ws.OwnerID != target.ID {
			return c.AbortBadRequest("workspace is not owned by this user")
		}
		if t.NewOwnerID == target.ID {
			return c.AbortBadRequest("cannot transfer a workspace to the account being deleted")
		}
		if _, err := h.workspaces.FindMember(ws.ID, t.NewOwnerID); err != nil {
			return c.AbortBadRequest("the new owner must already be a member of the workspace")
		}
		ws.OwnerID = t.NewOwnerID
		if err := h.workspaces.Update(ws); err != nil {
			return c.AbortInternalServerError("failed to transfer workspace", err)
		}

		_ = h.workspaces.UpdateMemberRole(ws.ID, t.NewOwnerID, models.WorkspaceRoleOwner)
		_ = h.workspaces.RemoveMember(ws.ID, target.ID) // the leaving owner steps out
		h.record(c, "admin.user.workspace_transfer", target.ID, map[string]any{"workspace": ws.ID, "new_owner": t.NewOwnerID})
		transferred++
	}

	deleteAt := time.Now().Add(time.Duration(h.graceDays) * 24 * time.Hour)
	target.ScheduledDeletionAt = &deleteAt
	if err := h.users.Update(target); err != nil {
		return c.AbortInternalServerError("failed to schedule deletion", err)
	}
	h.revokeAll(c, target.ID)
	h.record(c, "admin.user.deletion_scheduled", target.ID, map[string]any{
		"email": target.Email, "delete_at": deleteAt.UTC(), "transfers": transferred,
	})
	return ok(c, target)
}

// ForceDeletion skips the grace period; only for an account already pending deletion.
func (h *AdminUserHandler) ForceDeletion(c *okapi.Context) error {
	target, err := h.targetUser(c)
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if target.ID == middlewares.UserID(c) {
		return c.AbortBadRequest("you cannot delete your own account")
	}
	if blocked, err := h.guarded(c, target); blocked {
		return err
	}
	if target.ScheduledDeletionAt == nil {
		return c.AbortBadRequest("force deletion is only available for an account that is pending deletion")
	}
	if target.Role == models.SystemRoleAdmin {
		if last, _ := h.isLastAdmin(target.ID); last {
			return c.AbortBadRequest("cannot delete the last remaining admin")
		}
	}
	if h.account == nil {
		return c.AbortInternalServerError("failed to delete account", errAccountUnavailable)
	}
	res, err := h.account.PurgeAccount(c.Request().Context(), target.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to delete account", err)
	}
	h.revokeAll(c, target.ID)
	h.record(c, "admin.user.deletion_forced", target.ID, map[string]any{
		"email": target.Email, "workspaces": res.Workspaces, "apps": res.Apps, "databases": res.Databases,
	})
	return message(c, "account permanently deleted")
}

// CancelDeletion clears a pending deletion. The account stays disabled.
func (h *AdminUserHandler) CancelDeletion(c *okapi.Context) error {
	target, err := h.targetUser(c)
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if target.ScheduledDeletionAt == nil {
		return c.AbortBadRequest("no deletion is scheduled for this account")
	}
	target.ScheduledDeletionAt = nil
	if err := h.users.Update(target); err != nil {
		return c.AbortInternalServerError("failed to cancel deletion", err)
	}
	h.record(c, "admin.user.deletion_cancelled", target.ID, nil)
	return ok(c, target)
}

func (h *AdminUserHandler) VerifyEmail(c *okapi.Context) error {
	target, err := h.targetUser(c)
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if target.EmailVerifiedAt != nil {
		return c.AbortBadRequest("email is already verified")
	}
	now := time.Now()
	target.EmailVerifiedAt = &now
	if err := h.users.Update(target); err != nil {
		return c.AbortInternalServerError("failed to verify email", err)
	}
	h.record(c, "admin.user.email_verified", target.ID, nil)
	return ok(c, target)
}

// RevokeSessions invalidates all of a user's active sessions.
func (h *AdminUserHandler) RevokeSessions(c *okapi.Context) error {
	target, err := h.targetUser(c)
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	h.revokeAll(c, target.ID)
	h.record(c, "admin.user.revoke_sessions", target.ID, nil)
	return message(c, "sessions revoked")
}

// DisableTwoFactor is the account-recovery path; it needs no TOTP code since the caller is a platform admin.
func (h *AdminUserHandler) DisableTwoFactor(c *okapi.Context) error {
	target, err := h.targetUser(c)
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if target.IsService() {
		return c.AbortBadRequest(errServiceAccountManaged.Error())
	}
	if !target.TwoFactorEnabled {
		return c.AbortBadRequest("two-factor authentication is not enabled for this user")
	}
	target.TwoFactorEnabled = false
	target.TwoFactorSecret = ""
	if err := h.users.Update(target); err != nil {
		return c.AbortInternalServerError("failed to disable two-factor authentication", err)
	}
	h.db.Where("user_id = ?", target.ID).Delete(&models.TwoFactorRecoveryCode{})
	h.record(c, "admin.user.2fa_disabled", target.ID, nil)
	return message(c, "two-factor authentication disabled")
}

func (h *AdminUserHandler) ResetPasskeys(c *okapi.Context) error {
	target, err := h.targetUser(c)
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if target.IsService() {
		return c.AbortBadRequest(errServiceAccountManaged.Error())
	}
	removed, err := repositories.NewPasskeyRepository(h.db).DeleteAllForUser(target.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to remove passkeys", err)
	}
	if removed == 0 {
		return c.AbortBadRequest("this user has no passkeys")
	}
	h.revokeAll(c, target.ID)
	h.record(c, "admin.user.passkeys_reset", target.ID, map[string]any{"passkeys_removed": removed})
	return message(c, "passkeys removed")
}

// Returned exactly once: Miabi stores only the bcrypt hash.
type AdminResetPasswordResponse struct {
	Password string `json:"password"`
}

// ResetPassword revokes all sessions and returns the new password once; the old one is unrecoverable.
func (h *AdminUserHandler) ResetPassword(c *okapi.Context) error {
	target, err := h.targetUser(c)
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if target.IsService() {
		return c.AbortBadRequest(errServiceAccountManaged.Error())
	}
	pw, err := generatePassword()
	if err != nil {
		return c.AbortInternalServerError("failed to generate a password", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return c.AbortInternalServerError("failed to hash password", err)
	}
	target.PasswordHash = string(hash)
	target.MustChangePassword = true // the generated password is temporary
	if err := h.users.Update(target); err != nil {
		return c.AbortInternalServerError("failed to reset password", err)
	}
	h.revokeAll(c, target.ID)
	h.record(c, "admin.user.password_reset", target.ID, nil)
	return ok(c, AdminResetPasswordResponse{Password: pw})
}

func generatePassword() (string, error) {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	const n = 20
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i, b := range buf {
		buf[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(buf), nil
}

var errServiceAccountManaged = errors.New("this is a service account: it has no password or two-factor, " +
	"and cannot be a platform admin — manage it from its workspace's service accounts")

func (h *AdminUserHandler) targetUser(c *okapi.Context) (*models.User, error) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return nil, errInvalidID
	}
	return h.users.FindByID(uint(id))
}

func (h *AdminUserHandler) isLastAdmin(excludeID uint) (bool, error) {
	count, err := h.users.CountByRole(models.SystemRoleAdmin)
	if err != nil {
		return false, err
	}
	return count <= 1, nil
}

func (h *AdminUserHandler) revokeAll(c *okapi.Context, userID uint) {
	sessions, err := h.sessions.ListByUser(userID)
	if err != nil {
		return
	}
	ctx := c.Request().Context()
	for _, s := range sessions {
		if s.Revoked || time.Now().After(s.ExpiresAt) {
			continue
		}
		h.store.MarkRevoked(ctx, s.JTI, s.ExpiresAt)
		_ = h.sessions.RevokeByJTI(s.JTI)
	}
}

func (h *AdminUserHandler) record(c *okapi.Context, action string, targetID uint, meta map[string]any) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID:    &actor,
		Action:     action,
		TargetType: "user",
		TargetID:   strconv.Itoa(int(targetID)),
		IP:         c.RealIP(),
		Metadata:   meta,
	})
}

// AdminSuspendUserRequest suspends a user.
type AdminSuspendUserRequest struct {
	Body struct {
		Reason string     `json:"reason" maxLength:"500"`
		Until  *time.Time `json:"until"`
	} `json:"body"`
}

// Suspend keeps read access but refuses every write until lifted or expired; nothing is deleted.
func (h *AdminUserHandler) Suspend(c *okapi.Context, req *AdminSuspendUserRequest) error {
	target, err := h.targetUser(c)
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if target.ID == middlewares.UserID(c) {
		return c.AbortBadRequest("you cannot suspend yourself")
	}
	if blocked, err := h.guarded(c, target); blocked {
		return err
	}
	if target.Role == models.SystemRoleAdmin {
		if last, err := h.isLastAdmin(target.ID); err != nil || last {
			return c.AbortWithError(http.StatusConflict, errors.New("the last platform admin cannot be suspended"))
		}
	}
	now := time.Now().UTC()
	if req.Body.Until != nil && !req.Body.Until.After(now) {
		return c.AbortBadRequest("until must be in the future")
	}
	actor := middlewares.UserID(c)
	target.SuspendedAt, target.SuspendedReason, target.SuspendedUntil, target.SuspendedBy = &now, strings.TrimSpace(req.Body.Reason), req.Body.Until, &actor
	if err := h.users.Update(target); err != nil {
		return c.AbortInternalServerError("failed to suspend user", err)
	}
	h.record(c, "admin.user.suspend", target.ID, map[string]any{"reason": target.SuspendedReason, "until": req.Body.Until, "request_reason": c.Header("X-Request-Reason")})
	return ok(c, target)
}

// Unsuspend lifts a user's suspension.
func (h *AdminUserHandler) Unsuspend(c *okapi.Context) error {
	target, err := h.targetUser(c)
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	target.SuspendedAt, target.SuspendedReason, target.SuspendedUntil, target.SuspendedBy = nil, "", nil, nil
	if err := h.users.Update(target); err != nil {
		return c.AbortInternalServerError("failed to lift the suspension", err)
	}
	h.record(c, "admin.user.unsuspend", target.ID, map[string]any{"request_reason": c.Header("X-Request-Reason")})
	return ok(c, target)
}
