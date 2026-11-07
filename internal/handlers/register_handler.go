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
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/auth"
	"github.com/miabi-io/miabi/internal/services/mailer"
	"github.com/miabi-io/miabi/internal/services/registration"
	"github.com/miabi-io/miabi/internal/services/workspace"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"golang.org/x/crypto/bcrypt"
)

// RegisterHandler owns self-service sign-up.
type RegisterHandler struct {
	reg        *registration.Service
	auth       *auth.Service
	users      *repositories.UserRepository
	mailer     *mailer.Service
	audit      *audit.Logger
	workspaces *workspace.Service
	// ssoEnforced reports whether password login is disabled for a user; see AuthHandler.SetSSOEnforcement.
	ssoEnforced func(*models.User) bool
}

// SetInvitations wires sign-up from a workspace invitation (nil-safe: the endpoints refuse).
func (h *RegisterHandler) SetInvitations(ws *workspace.Service, ssoEnforced func(*models.User) bool) {
	h.workspaces, h.ssoEnforced = ws, ssoEnforced
}

func NewRegisterHandler(reg *registration.Service, a *auth.Service, users *repositories.UserRepository, m *mailer.Service, auditLog *audit.Logger) *RegisterHandler {
	return &RegisterHandler{reg: reg, auth: a, users: users, mailer: m, audit: auditLog}
}

// RegisterRequest is the sign-up form.
type RegisterRequest struct {
	Body struct {
		Name     string `json:"name" required:"true"`
		Email    string `json:"email" required:"true"`
		Password string `json:"password" required:"true"`
	} `json:"body"`
}

// RegisterResponse tells the caller what to do next, which differs by policy.
type RegisterResponse struct {
	VerificationRequired bool   `json:"verification_required"`
	Message              string `json:"message"`
}

// Register creates an account when self-service sign-up is open.
func (h *RegisterHandler) Register(c *okapi.Context, req *RegisterRequest) error {
	email := strings.ToLower(strings.TrimSpace(req.Body.Email))
	name := strings.TrimSpace(req.Body.Name)

	policy := h.reg.Check(email)
	if policy != nil && !errors.Is(policy, registration.ErrDomainNotAllowed) {
		return c.AbortWithError(403, policy)
	}
	if name == "" {
		return c.AbortBadRequest("a name is required")
	}
	if !strings.Contains(email, "@") {
		return c.AbortBadRequest("a valid email address is required")
	}
	if err := models.CheckNewPassword(req.Body.Password); err != nil {
		return c.AbortBadRequest(err.Error())
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Body.Password), bcrypt.DefaultCost)
	if err != nil {
		return c.AbortInternalServerError("failed to hash password", err)
	}

	exists, err := h.users.ExistsByEmail(email)
	if err != nil {
		return c.AbortInternalServerError("failed to check email", err)
	}
	if exists || policy != nil {
		return h.declineQuietly(c, email, policy)
	}

	user := &models.User{
		Name:         name,
		Email:        email,
		PasswordHash: string(hash),
		Role:         models.SystemRoleUser,
		Active:       true,
		SignupSource: models.SignupSourceSelf,
	}
	if !h.reg.RequiresVerification() {
		now := time.Now()
		user.EmailVerifiedAt = &now
	}
	if err := h.users.Create(user); err != nil {
		// A racing duplicate must look like any other duplicate, not a 500.
		if dup, dupErr := h.users.ExistsByEmail(email); dupErr == nil && dup {
			return h.declineQuietly(c, email, nil)
		}
		return c.AbortInternalServerError("failed to create account", err)
	}

	h.audit.Record(audit.Entry{
		ActorID: &user.ID, Action: "auth.register", TargetType: "user",
		TargetID: user.Username, IP: c.RealIP(),
		Metadata: map[string]any{"email": user.Email, "verification_required": h.reg.RequiresVerification()},
	})
	if h.reg.RequiresVerification() {
		h.sendVerification(user)
	} else if h.mailer != nil {
		h.mailer.SendWelcome(user.Email, user.Name)
	}
	return ok(c, h.pendingResponse())
}

// Best-effort: an admin can still verify the account by hand.
func (h *RegisterHandler) sendVerification(user *models.User) {
	if h.auth == nil || h.mailer == nil {
		return
	}
	token, err := h.auth.CreateEmailVerification(user)
	if err != nil {
		logger.Error("could not issue an email verification link", "user", user.ID, "error", err)
		return
	}
	h.mailer.SendEmailVerification(user.Email, user.Name, token, int(auth.EmailVerificationTTL.Hours()))
}

// VerifyEmail consumes a verification link.
func (h *RegisterHandler) VerifyEmail(c *okapi.Context, req *VerifyEmailRequest) error {
	if h.auth == nil {
		return c.AbortBadRequest("email verification is not available")
	}
	user, err := h.auth.ConfirmEmailVerification(strings.TrimSpace(req.Body.Token))
	if err != nil {
		// One answer for expired, spent and never-existed, so probing tokens learns nothing.
		return c.AbortBadRequest("that verification link is invalid or has expired")
	}
	h.audit.Record(audit.Entry{
		ActorID: &user.ID, Action: "auth.email_verified", TargetType: "user",
		TargetID: user.Username, IP: c.RealIP(),
	})
	if h.mailer != nil {
		h.mailer.SendWelcome(user.Email, user.Name)
	}
	return ok(c, RegisterResponse{Message: "Your email is verified. You can sign in now."})
}

// VerifyEmailRequest carries the token from the emailed link.
type VerifyEmailRequest struct {
	Body struct {
		Token string `json:"token" required:"true"`
	} `json:"body"`
}

func (h *RegisterHandler) declineQuietly(c *okapi.Context, email string, policy error) error {
	reason := "email_taken"
	if errors.Is(policy, registration.ErrDomainNotAllowed) {
		reason = "domain_not_allowed"
	}
	h.audit.Record(audit.Entry{
		Action: "auth.register.declined", TargetType: "user", TargetID: email,
		IP: c.RealIP(), Metadata: map[string]any{"reason": reason},
	})
	return ok(c, h.pendingResponse())
}

// A real and a declined sign-up must be indistinguishable from outside.
func (h *RegisterHandler) pendingResponse() RegisterResponse {
	if h.reg.RequiresVerification() {
		return RegisterResponse{
			VerificationRequired: true,
			Message:              "Check your email to verify your address, then sign in.",
		}
	}
	return RegisterResponse{Message: "Your account is ready. You can sign in now."}
}

var errInvalidInvitation = errors.New("this invitation is invalid or has expired")

// InvitationTokenRequest carries an invitation token in the body, keeping it out of access logs.
type InvitationTokenRequest struct {
	Body struct {
		Token string `json:"token" required:"true"`
	} `json:"body"`
}

type InvitationPreviewResponse struct {
	workspace.InvitationPreview
	// SignupAllowed means an invitee without an account may create one from this invitation.
	SignupAllowed bool `json:"signup_allowed"`
	// SSORequired means the account must come from single sign-on, not a password.
	SSORequired bool `json:"sso_required"`
}

// PreviewInvitation describes a pending invitation. One answer for unknown, expired and used tokens.
func (h *RegisterHandler) PreviewInvitation(c *okapi.Context, req *InvitationTokenRequest) error {
	if h.workspaces == nil {
		return c.AbortNotFound(errInvalidInvitation.Error())
	}
	p, err := h.workspaces.PreviewInvitation(strings.TrimSpace(req.Body.Token))
	if err != nil {
		return c.AbortNotFound(errInvalidInvitation.Error())
	}
	sso := h.invitationSSORequired(p.OrganizationID)
	return ok(c, InvitationPreviewResponse{
		InvitationPreview: *p,
		SSORequired:       sso,
		SignupAllowed:     !p.AccountExists && !sso && h.reg.InvitationSignupEnabled(),
	})
}

func (h *RegisterHandler) invitationSSORequired(orgID uint) bool {
	if h.ssoEnforced == nil {
		return false
	}
	u := &models.User{Role: models.SystemRoleUser, Kind: models.UserKindHuman}
	if orgID != 0 {
		u.OrganizationID = &orgID
	}
	return h.ssoEnforced(u)
}

// The email is the invited one and cannot be chosen.
type RegisterFromInvitationRequest struct {
	Body struct {
		Token    string `json:"token" required:"true"`
		Name     string `json:"name" required:"true"`
		Password string `json:"password" required:"true"`
	} `json:"body"`
}

// RegisterFromInvitationResponse names the workspace the new account joined.
type RegisterFromInvitationResponse struct {
	WorkspaceID   uint   `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	Message       string `json:"message"`
}

// Open while self-service sign-up is closed: the invitation is the authorization.
func (h *RegisterHandler) RegisterFromInvitation(c *okapi.Context, req *RegisterFromInvitationRequest) error {
	if h.workspaces == nil || !h.reg.InvitationSignupEnabled() {
		return c.AbortWithError(403, errors.New("signing up from an invitation is not available on this platform; ask an administrator to create your account"))
	}
	token := strings.TrimSpace(req.Body.Token)
	name := strings.TrimSpace(req.Body.Name)
	if name == "" {
		return c.AbortBadRequest("a name is required")
	}
	if err := models.CheckNewPassword(req.Body.Password); err != nil {
		return c.AbortBadRequest(err.Error())
	}
	p, err := h.workspaces.PreviewInvitation(token)
	if err != nil {
		return c.AbortBadRequest(errInvalidInvitation.Error())
	}
	if h.invitationSSORequired(p.OrganizationID) {
		return c.AbortWithError(403, errors.New("this organization signs in with single sign-on; use it to create your account"))
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Body.Password), bcrypt.DefaultCost)
	if err != nil {
		return c.AbortInternalServerError("failed to hash password", err)
	}
	// The token reached the invited mailbox, which proves the address.
	now := time.Now()
	user := &models.User{
		Name: name, PasswordHash: string(hash), Role: models.SystemRoleUser,
		Active: true, EmailVerifiedAt: &now,
	}
	ws, err := h.workspaces.RegisterFromInvitation(token, user)
	if err != nil {
		var qe interface{ Code() string }
		if errors.As(err, &qe) {
			return c.AbortWithError(403, err)
		}
		if errors.Is(err, workspace.ErrInvalidInvite) {
			return c.AbortBadRequest(errInvalidInvitation.Error())
		}
		return c.AbortInternalServerError("failed to create account", err)
	}
	h.audit.Record(audit.Entry{
		ActorID: &user.ID, Action: "auth.register", TargetType: "user",
		TargetID: user.Username, IP: c.RealIP(),
		Metadata: map[string]any{"email": user.Email, "source": models.SignupSourceInvitation, "workspace_id": ws.ID},
	})
	h.audit.Record(audit.Entry{
		ActorID: &user.ID, WorkspaceID: &ws.ID, Action: "workspace.invite_accept",
		TargetType: "workspace", TargetID: strconv.Itoa(int(ws.ID)), IP: c.RealIP(),
	})
	if h.mailer != nil {
		h.mailer.SendWelcome(user.Email, user.Name)
	}
	return created(c, RegisterFromInvitationResponse{
		WorkspaceID: ws.ID, WorkspaceName: ws.DisplayName,
		Message: "Your account is ready. Sign in to open " + ws.DisplayName + ".",
	})
}
