// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"crypto/rand"
	"errors"
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
	graceDays  int
}

// SetMailer wires the platform mailer used to email a welcome to admin-created
// users. Optional; without it (or without SMTP configured) the email is skipped.
func (h *AdminUserHandler) SetMailer(m *mailer.Service) { h.mailer = m }

// SetEnterprise wires the edition gate used to guard the per-user workspace-limit
// override (the user_workspace_limit entitlement).
func (h *AdminUserHandler) SetEnterprise(ee enterprise.EE) { h.ee = ee }

// AdminSetWorkspaceLimitRequest sets or clears a user's workspace-count override.
type AdminSetWorkspaceLimitRequest struct {
	Body struct {
		// Limit overrides how many workspaces the user may own. null clears the
		// override (inherit the platform max_workspaces_per_user); -1 = unlimited;
		// 0 = none; N = at most N.
		Limit *int `json:"limit"`
	} `json:"body"`
}

// SetWorkspaceLimit sets or clears a user's per-user workspace-count override
// (Enterprise: user_workspace_limit). null clears it (the user then inherits the
// platform-global max_workspaces_per_user).
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

// requireLimitOverride returns the entitlement error for the workspace-limit
// override, tolerating a nil edition gate (treated as community).
func (h *AdminUserHandler) requireLimitOverride() error {
	if h.ee == nil {
		return enterprise.ErrLicenseRequired
	}
	return h.ee.RequireMutable(enterprise.FlagUserWorkspaceLimit)
}

// AdminSetWorkspaceMembershipLimitRequest sets or clears a user's membership
// (workspaces-joined) override.
type AdminSetWorkspaceMembershipLimitRequest struct {
	Body struct {
		// Limit overrides how many workspaces the user may join as a non-owner
		// member. null clears the override (inherit max_workspace_memberships_per_user);
		// -1 = unlimited; 0 = none; N = at most N.
		Limit *int `json:"limit"`
	} `json:"body"`
}

// SetWorkspaceMembershipLimit sets or clears a user's per-user membership override
// (Enterprise: user_workspace_membership_limit). null clears it (inherit the
// platform-global max_workspace_memberships_per_user).
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

// requireMembershipOverride returns the entitlement error for the membership
// override, tolerating a nil edition gate (treated as community).
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

// --- DTOs ---

type AdminCreateUserRequest struct {
	Body struct {
		Name string `json:"name" required:"true"`
		// Username optionally sets the unique handle; auto-derived from the email
		// local-part when omitted. Slug-validated; reserved/taken handles rejected.
		Username string `json:"username"`
		Email    string `json:"email" required:"true" format:"email"`
		Password string `json:"password" required:"true" minLength:"8"`
		Role     string `json:"role" enum:"admin,user"`
		// Notify, when true, emails the new user a welcome with a sign-in link
		// (requires a configured system SMTP server).
		Notify bool `json:"notify"`
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

// --- Handlers ---

// List returns a paginated, searchable list of users.
func (h *AdminUserHandler) List(c *okapi.Context) error {
	page, size, offset := normalizePageParams(queryInt(c, "page", 0), queryInt(c, "size", 20))
	users, total, err := h.users.List(c.Query("search"), size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list users", err)
	}
	return paginated(c, users, total, page, size)
}

// MemberSummary is another member of a workspace — a candidate to receive
// ownership when the owner's account is deleted.
type MemberSummary struct {
	UserID uint   `json:"user_id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Role   string `json:"role"`
}

// WorkspaceSummary is one owned workspace with its resource counts. Members lists
// the workspace's OTHER members (ownership-transfer candidates); when empty, the
// workspace can only be deleted with the account.
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

	// Initialise the slices so they marshal as [] rather than null — the web UI
	// reads .length on them and a null would break the page for users with no
	// workspaces or no recorded activity.
	detail := AdminUserDetail{
		User:             *user,
		WorkspacesOwned:  len(owned),
		WorkspacesMember: memberships,
		OwnedWorkspaces:  []WorkspaceSummary{},
		RecentEvents:     []models.AuditLog{},
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

// countIn counts rows in the given workspaces, optionally filtered by app status.
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
	// Validate an explicit handle up front; a blank one is auto-derived by the
	// User.BeforeCreate hook.
	username, err := validateUsername(h.users, req.Body.Username, 0)
	if err != nil {
		if errors.Is(err, errUsernameTaken) {
			return c.AbortWithError(409, err)
		}
		return c.AbortBadRequest(err.Error())
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Body.Password), bcrypt.DefaultCost)
	if err != nil {
		return c.AbortInternalServerError("failed to hash password", err)
	}
	role := models.SystemRoleUser
	if req.Body.Role == string(models.SystemRoleAdmin) {
		role = models.SystemRoleAdmin
	}
	now := time.Now()
	user := &models.User{
		Name:            strings.TrimSpace(req.Body.Name),
		Username:        username,
		Email:           email,
		PasswordHash:    string(hash),
		Role:            role,
		Active:          true,
		EmailVerifiedAt: &now,
		// Admin-set password is temporary — force the user to set their own on first
		// login.
		MustChangePassword: true,
	}
	if err := h.users.Create(user); err != nil {
		return c.AbortInternalServerError("failed to create user", err)
	}
	h.record(c, "admin.user.create", user.ID, map[string]any{"email": user.Email, "role": string(role), "notified": req.Body.Notify})
	// Optionally email the new user a welcome (best-effort, async).
	if req.Body.Notify {
		h.mailer.SendWelcome(user.Email, user.Name)
	}
	return created(c, user)
}

// Update changes a user's role, active flag, and/or email-verified state.
func (h *AdminUserHandler) Update(c *okapi.Context, req *AdminUpdateUserRequest) error {
	target, err := h.targetUser(c)
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	self := middlewares.UserID(c)
	wasActive := target.Active // captured before the active flag is mutated below

	if req.Body.Role != "" && req.Body.Role != string(target.Role) {
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
	// Disabling an account stops all of its apps and databases (best-effort; the
	// account is already deactivated regardless of stop outcomes).
	if wasActive && !target.Active && h.account != nil {
		res := h.account.StopOwned(c.Request().Context(), target.ID)
		h.record(c, "admin.user.disable", target.ID, map[string]any{"apps_stopped": res.Apps, "databases_stopped": res.Databases})
	}
	h.record(c, "admin.user.update", target.ID, map[string]any{"role": string(target.Role), "active": target.Active})
	return ok(c, target)
}

// AdminScheduleDeletionRequest optionally transfers ownership of some of the
// user's workspaces to another member before scheduling the account for deletion.
type AdminScheduleDeletionRequest struct {
	Body struct {
		Transfers []struct {
			WorkspaceID uint `json:"workspace_id" required:"true"`
			NewOwnerID  uint `json:"new_owner_id" required:"true"`
		} `json:"transfers"`
	} `json:"body"`
}

// ScheduleDeletion marks a disabled account for permanent deletion after the
// grace period. Owned workspaces whose ownership the admin chose to transfer are
// reassigned first (and so survive); everything still owned by the user is purged
// with the account by the daily job. A user must be disabled first; admins cannot
// schedule themselves or the last remaining admin.
func (h *AdminUserHandler) ScheduleDeletion(c *okapi.Context, req *AdminScheduleDeletionRequest) error {
	target, err := h.targetUser(c)
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if target.ID == middlewares.UserID(c) {
		return c.AbortBadRequest("you cannot delete your own account")
	}
	if target.Active {
		return c.AbortBadRequest("disable the account before scheduling deletion")
	}
	if target.Role == models.SystemRoleAdmin {
		if last, _ := h.isLastAdmin(target.ID); last {
			return c.AbortBadRequest("cannot delete the last remaining admin")
		}
	}

	// Apply ownership transfers up-front: a transferred workspace is no longer
	// owned by the user, so it won't be purged with the account.
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

// ForceDeletion permanently deletes an account immediately, skipping whatever
// remains of the grace period. It is only available for an account that is
// already pending deletion (ScheduledDeletionAt set) — i.e. one that was
// disabled and scheduled, with any ownership transfers already applied. The same
// guards as scheduling apply: an admin cannot force-delete their own account or
// the last remaining admin.
func (h *AdminUserHandler) ForceDeletion(c *okapi.Context) error {
	target, err := h.targetUser(c)
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if target.ID == middlewares.UserID(c) {
		return c.AbortBadRequest("you cannot delete your own account")
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

// VerifyEmail marks a user's email address as verified — a one-click admin
// action (e.g. when the user can't receive the verification mail).
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

// DisableTwoFactor clears a user's two-factor authentication. This is the
// account-recovery path for a user who has lost their authenticator; it needs
// no TOTP code since the caller is a platform admin.
func (h *AdminUserHandler) DisableTwoFactor(c *okapi.Context) error {
	target, err := h.targetUser(c)
	if err != nil {
		return c.AbortNotFound("user not found")
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

// AdminResetPasswordResponse carries the freshly generated password, returned
// exactly once so the admin can hand it to the user (Miabi never stores it in
// clear, only its bcrypt hash).
type AdminResetPasswordResponse struct {
	Password string `json:"password"`
}

// ResetPassword generates a new strong password for a user, replaces their
// current one, and returns it ONCE. All the user's sessions are revoked so the
// old credential stops working immediately. This is the admin account-recovery
// path and is irreversible: the previous password is discarded, not recoverable.
func (h *AdminUserHandler) ResetPassword(c *okapi.Context) error {
	target, err := h.targetUser(c)
	if err != nil {
		return c.AbortNotFound("user not found")
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
	// The old password is gone; sign the user out everywhere so no stale session
	// keeps the account open past the reset.
	h.revokeAll(c, target.ID)
	h.record(c, "admin.user.password_reset", target.ID, nil)
	return ok(c, AdminResetPasswordResponse{Password: pw})
}

// generatePassword returns a strong random password from an unambiguous alphabet
// (no 0/O/1/l/I), backed by crypto/rand — used for admin-initiated resets.
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

// --- helpers ---

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
