// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/auth"
	"github.com/miabi-io/miabi/internal/services/mailer"
	"github.com/miabi-io/miabi/internal/services/registration"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"golang.org/x/crypto/bcrypt"
)

// MinRegistrationPasswordLen is the floor for a self-chosen password. An account
// created this way is not handed a temporary one by an admin, so this is the only
// gate on it.
const MinRegistrationPasswordLen = 12

// RegisterHandler owns self-service sign-up.
type RegisterHandler struct {
	reg    *registration.Service
	auth   *auth.Service
	users  *repositories.UserRepository
	mailer *mailer.Service
	audit  *audit.Logger
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
	// VerificationRequired means the account exists but cannot sign in until the
	// address is verified.
	VerificationRequired bool   `json:"verification_required"`
	Message              string `json:"message"`
}

// Register creates an account when self-service sign-up is open.
func (h *RegisterHandler) Register(c *okapi.Context, req *RegisterRequest) error {
	email := strings.ToLower(strings.TrimSpace(req.Body.Email))
	name := strings.TrimSpace(req.Body.Name)

	// Policy first, before anything is read or written: a closed platform should do
	// no work at all on an anonymous request.
	if err := h.reg.Check(email); err != nil {
		return c.AbortWithError(403, err)
	}
	if name == "" {
		return c.AbortBadRequest("a name is required")
	}
	if !strings.Contains(email, "@") {
		return c.AbortBadRequest("a valid email address is required")
	}
	if len([]rune(req.Body.Password)) < MinRegistrationPasswordLen {
		return c.AbortBadRequest("password must be at least 12 characters")
	}

	exists, err := h.users.ExistsByEmail(email)
	if err != nil {
		return c.AbortInternalServerError("failed to check email", err)
	}
	if exists {
		// Deliberately the same shape of answer as success. Telling an anonymous
		// caller that an address is registered turns sign-up into an account
		// oracle: anyone could enumerate who has one.
		return ok(c, h.pendingResponse())
	}

	username, err := validateUsername(h.users, "", 0)
	if err != nil {
		return c.AbortInternalServerError("failed to allocate a username", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Body.Password), bcrypt.DefaultCost)
	if err != nil {
		return c.AbortInternalServerError("failed to hash password", err)
	}

	user := &models.User{
		Name:         name,
		Username:     username,
		Email:        email,
		PasswordHash: string(hash),
		// Self-service sign-up never grants admin, whatever else is configured.
		Role:   models.SystemRoleUser,
		Active: true,
	}
	if !h.reg.RequiresVerification() {
		now := time.Now()
		user.EmailVerifiedAt = &now
	}
	if err := h.users.Create(user); err != nil {
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

// sendVerification issues a link and mails it. Best-effort: the account exists
// either way, and an admin can still verify it by hand from the user detail page.
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
		// One answer for expired, spent and never-existed: a caller probing tokens
		// learns nothing from the difference.
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

// pendingResponse is what both a real sign-up and a duplicate address return, so
// the two cannot be told apart from outside.
func (h *RegisterHandler) pendingResponse() RegisterResponse {
	if h.reg.RequiresVerification() {
		return RegisterResponse{
			VerificationRequired: true,
			Message:              "Check your email to verify your address, then sign in.",
		}
	}
	return RegisterResponse{Message: "Your account is ready. You can sign in now."}
}
