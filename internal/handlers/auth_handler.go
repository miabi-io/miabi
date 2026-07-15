// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/auth"
	"github.com/miabi-io/miabi/internal/services/directory"
	"github.com/miabi-io/miabi/internal/services/logintoken"
	"github.com/miabi-io/miabi/internal/services/mailer"
	"github.com/miabi-io/miabi/internal/services/settings"
	"github.com/miabi-io/miabi/internal/services/twofactor"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

type AuthHandler struct {
	auth     *auth.Service
	users    *repositories.UserRepository
	sessions *repositories.SessionRepository
	audit    *audit.Logger
	settings *settings.Provider
	mailer   *mailer.Service
	devMode  bool
	// passwordResetEnabled gates the self-service "forgot password" flow. A
	// critical auth control, so it is fixed at boot from MIABI_PASSWORD_RESET_ENABLED
	// rather than a runtime setting — changing it requires a restart.
	passwordResetEnabled bool
	// enforceSSO reports whether a user must sign in via SSO (password login
	// blocked). Set by SetSSOEnforcement; nil means never enforced. Platform
	// admins are exempt by the closure so a misconfigured IdP can't lock everyone
	// out.
	enforceSSO func(user *models.User) bool
	// directoryLogin attempts LDAP/AD auth as a fall-through when local password
	// auth fails (Enterprise). Set by SetDirectoryLogin; nil means no directory.
	// Returns (user,nil) on success, (nil,nil) to fall through, (nil,err) on a
	// bind/disabled failure.
	directoryLogin func(ctx context.Context, identifier, password string) (*models.User, error)
	// loginTokens mints the short-lived personal API token behind the "Copy login
	// command" flow. Set by SetLoginTokens; nil disables the endpoint.
	loginTokens *logintoken.Service
}

// SetLoginTokens wires the CLI login-token issuer (the "Copy login command"
// flow). Optional; without it POST /auth/login-token returns 503.
func (h *AuthHandler) SetLoginTokens(s *logintoken.Service) { h.loginTokens = s }

// SetSSOEnforcement wires the enforced-SSO predicate (Enterprise). Optional.
func (h *AuthHandler) SetSSOEnforcement(fn func(user *models.User) bool) {
	h.enforceSSO = fn
}

// SetDirectoryLogin wires the LDAP/AD fall-through authenticator (Enterprise).
// Optional; without it login is local-password + redirect-SSO only.
func (h *AuthHandler) SetDirectoryLogin(fn func(ctx context.Context, identifier, password string) (*models.User, error)) {
	h.directoryLogin = fn
}

// SetMailer wires the platform mailer used to deliver password-reset emails.
// Optional; without it (or without SMTP configured) the email is skipped.
func (h *AuthHandler) SetMailer(m *mailer.Service) { h.mailer = m }

func NewAuthHandler(a *auth.Service, users *repositories.UserRepository, sessions *repositories.SessionRepository, auditLog *audit.Logger, settingsProvider *settings.Provider, devMode, passwordResetEnabled bool) *AuthHandler {
	return &AuthHandler{auth: a, users: users, sessions: sessions, audit: auditLog, settings: settingsProvider, devMode: devMode, passwordResetEnabled: passwordResetEnabled}
}

// --- DTOs ---

type LoginRequest struct {
	Body struct {
		// Username is the login handle — a username or an email address.
		Username string `json:"username" required:"true"`
		Password string `json:"password" required:"true"`
		// TwoFactorCode is the TOTP code, required only on the second login step
		// when the account has two-factor authentication enabled.
		TwoFactorCode string `json:"two_factor_code"`
	} `json:"body"`
}

// LoginTokenRequest re-authenticates the user (username + password, and a TOTP
// code when 2FA is on) to mint a short-lived personal API token for the CLI. It
// deliberately takes credentials in the body — the token is never issued off an
// ambient session, so the flow re-authenticates even a signed-in user.
type LoginTokenRequest struct {
	Body struct {
		Username      string `json:"username" required:"true"`
		Password      string `json:"password" required:"true"`
		TwoFactorCode string `json:"two_factor_code"`
		// Scopes optionally narrows the token; admin/"*" are rejected. Empty
		// defaults to read/write/deploy (console-equivalent).
		Scopes []string `json:"scopes"`
		// ExpiresInHours optionally overrides the default lifetime, capped server-side.
		ExpiresInHours *int `json:"expires_in_hours"`
	} `json:"body"`
}

// LoginTokenResponse carries the freshly minted token and the ready-to-paste CLI
// and curl commands (shown once). TwoFactorRequired mirrors login: a valid
// password on a 2FA account returns a challenge to re-submit with a code.
type LoginTokenResponse struct {
	Token             string     `json:"token,omitempty"`
	SHA256            string     `json:"sha256,omitempty"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
	Scopes            []string   `json:"scopes,omitempty"`
	ServerURL         string     `json:"server_url,omitempty"`
	LoginCommand      string     `json:"login_command,omitempty"`
	CurlExample       string     `json:"curl_example,omitempty"`
	TwoFactorRequired bool       `json:"two_factor_required,omitempty"`
}

// TokenPayload converts a minted login token to the API response shape.
func tokenPayload(t *logintoken.Token) LoginTokenResponse {
	exp := t.ExpiresAt
	return LoginTokenResponse{
		Token: t.Token, SHA256: t.SHA256, ExpiresAt: &exp, Scopes: t.Scopes,
		ServerURL: t.ServerURL, LoginCommand: t.LoginCommand, CurlExample: t.CurlExample,
	}
}

type ForgotPasswordRequest struct {
	Body struct {
		Email string `json:"email" required:"true" format:"email"`
	} `json:"body"`
}

type ResetPasswordRequest struct {
	Body struct {
		Token    string `json:"token" required:"true"`
		Password string `json:"password" required:"true" minLength:"8"`
	} `json:"body"`
}

type ChangePasswordRequest struct {
	Body struct {
		CurrentPassword string `json:"current_password" required:"true"`
		NewPassword     string `json:"new_password" required:"true" minLength:"8"`
	} `json:"body"`
}

// UpdateProfileRequest carries the editable fields of a user's own profile.
type UpdateProfileRequest struct {
	Body struct {
		Name string `json:"name" required:"true" minLength:"1" maxLength:"100"`
		// Username optionally changes the unique handle. Omitted leaves it
		// unchanged; when set it is slug-validated, reserved words and taken
		// handles are rejected.
		Username string `json:"username"`
	} `json:"body"`
}

// SessionInfo describes one active sign-in session for the /me/sessions list.
// Current flags the session the request itself is authenticated with.
type SessionInfo struct {
	ID        uint      `json:"id"`
	IPAddress string    `json:"ip_address"`
	UserAgent string    `json:"user_agent"`
	Current   bool      `json:"current"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// RevokeOthersResult reports how many other sessions were signed out.
type RevokeOthersResult struct {
	Message string `json:"message"`
	Revoked int    `json:"revoked"`
}

type UserProfile struct {
	ID               uint              `json:"id"`
	Name             string            `json:"name"`
	Username         string            `json:"username"`
	Email            string            `json:"email"`
	Role             models.SystemRole `json:"role"`
	TwoFactorEnabled bool              `json:"two_factor_enabled"`
	// OnboardingDismissed is true once the user has dismissed or completed the
	// getting-started checklist; the web hides onboarding guidance when set.
	OnboardingDismissed bool `json:"onboarding_dismissed"`
	// RecoveryCodesRemaining is populated on the profile (/me) endpoint only.
	RecoveryCodesRemaining *int `json:"recovery_codes_remaining,omitempty"`
	// Auth describes the credential the request authenticated with. Populated on
	// /me only, so a CLI/Terraform can discover the workspace its token manages.
	Auth *AuthContext `json:"auth,omitempty"`
}

// AuthContext reports the principal behind the current request: how it
// authenticated and, for a workspace-bound API key, which workspace the token is
// scoped to (nil = account-wide / session).
type AuthContext struct {
	Method      string   `json:"method"`                 // "jwt" | "api_key"
	APIKeyID    *uint    `json:"api_key_id,omitempty"`   // nil for session callers
	WorkspaceID *uint    `json:"workspace_id,omitempty"` // the key's bound workspace
	Scopes      []string `json:"scopes,omitempty"`
}

type AuthResponse struct {
	Token string       `json:"token,omitempty"`
	User  *UserProfile `json:"user,omitempty"`
	// TwoFactorRequired is true when the credentials were valid but the account
	// needs a TOTP code; the client should re-submit login with two_factor_code.
	TwoFactorRequired bool `json:"two_factor_required,omitempty"`
	// MustChangePassword is true when the credentials were valid but the account
	// has an admin-set/reset password it must replace. No session is issued;
	// ResetToken is a short-lived, single-use token the client exchanges — via
	// /auth/complete-password-reset with a new password — for a real session.
	MustChangePassword bool   `json:"must_change_password,omitempty"`
	ResetToken         string `json:"reset_token,omitempty"`
}

// Setup2FAResponse carries the new TOTP secret, its otpauth:// URL, and a
// ready-to-render QR code (PNG data URI) for the authenticator app.
type Setup2FAResponse struct {
	Secret string `json:"secret"`
	URL    string `json:"url"`
	QRCode string `json:"qr_code"`
}

// RecoveryCodesResponse carries freshly generated single-use backup codes,
// shown to the user once.
type RecoveryCodesResponse struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

type Verify2FARequest struct {
	Body struct {
		Code string `json:"code" required:"true" minLength:"6" maxLength:"6"`
	} `json:"body"`
}

// Disable2FARequest / RegenerateCodesRequest accept either a 6-digit TOTP code
// or a recovery code, so the field is not length-constrained.
type Disable2FARequest struct {
	Body struct {
		Code string `json:"code" required:"true"`
	} `json:"body"`
}

type RegenerateCodesRequest struct {
	Body struct {
		Code string `json:"code" required:"true" minLength:"6" maxLength:"6"`
	} `json:"body"`
}

// AuthStatus advertises which auth features are enabled, so the login and
// register screens can render conditionally.
type AuthStatus struct {
	PasswordResetEnabled bool `json:"password_reset_enabled"`
}

func profileOf(u *models.User) UserProfile {
	return UserProfile{ID: u.ID, Name: u.Name, Username: u.Username, Email: u.Email, Role: u.Role, TwoFactorEnabled: u.TwoFactorEnabled, OnboardingDismissed: u.OnboardingDismissedAt != nil}
}

// --- Handlers ---

// Status reports which auth features are enabled (public). Registration is
// closed — the platform admin is seeded at install; accounts are created by an
// admin from the Users page.
func (h *AuthHandler) Status(c *okapi.Context) error {
	return ok(c, AuthStatus{
		PasswordResetEnabled: h.passwordResetEnabled,
	})
}

// errEmailUnverified / errSSORequired are credential-check policy failures,
// mapped to 403 by abortCredential. They keep the shared check independent of the
// two callers (Login, LoginToken).
var (
	errEmailUnverified = errors.New("email address is not verified")
	errSSORequired     = errors.New("password login is disabled for this organization; use single sign-on")
)

// credentialCheck runs the shared login gate: local password auth with the
// LDAP/AD fall-through, then the disabled / email-verification / enforced-SSO
// policy. It returns the user and whether auth came via the directory. 2FA and
// the success path are handled per-caller (a session for Login, a token for
// LoginToken). The returned error is passed to abortCredential.
func (h *AuthHandler) credentialCheck(ctx context.Context, identifier, password string) (*models.User, bool, error) {
	user, err := h.auth.Authenticate(identifier, password)
	viaDirectory := false
	if err != nil && h.directoryLogin != nil {
		du, derr := h.directoryLogin(ctx, identifier, password)
		switch {
		case derr != nil:
			err = derr
		case du != nil:
			user, err, viaDirectory = du, nil, true
		}
	}
	if err != nil {
		return nil, false, err
	}
	if h.settings.Bool(settings.KeyRequireEmailVerification, false) && user.EmailVerifiedAt == nil && !user.IsAdmin() {
		return nil, false, errEmailUnverified
	}
	// Enforced SSO blocks LOCAL password login (platform admins exempt — a
	// lock-out safety valve). A directory login is itself an accepted SSO method.
	if !viaDirectory && h.enforceSSO != nil && h.enforceSSO(user) {
		return nil, false, errSSORequired
	}
	return user, viaDirectory, nil
}

// abortCredential maps a credentialCheck error to the same HTTP responses Login
// has always returned.
func (h *AuthHandler) abortCredential(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, auth.ErrAccountDisabled), errors.Is(err, directory.ErrAccountDisabled):
		return c.AbortForbidden("account is disabled")
	case errors.Is(err, errEmailUnverified):
		return c.AbortForbidden("email address is not verified")
	case errors.Is(err, errSSORequired):
		return c.AbortForbidden(err.Error())
	default:
		return c.AbortUnauthorized("invalid credentials")
	}
}

// Login authenticates a user and returns an access token.
func (h *AuthHandler) Login(c *okapi.Context, req *LoginRequest) error {
	identifier := strings.TrimSpace(req.Body.Username)
	if identifier == "" {
		return c.AbortBadRequest("a username or email is required")
	}
	user, viaDirectory, err := h.credentialCheck(c.Request().Context(), identifier, req.Body.Password)
	if err != nil {
		return h.abortCredential(c, err)
	}
	// Second factor: when enabled, the first request (no code) returns a
	// challenge; the client re-submits the same credentials plus the TOTP code.
	if user.TwoFactorEnabled {
		if req.Body.TwoFactorCode == "" {
			return ok(c, AuthResponse{TwoFactorRequired: true})
		}
		if !h.auth.VerifyLoginCode(user, req.Body.TwoFactorCode) {
			h.audit.Record(audit.Entry{ActorID: &user.ID, Action: "user.login_2fa_failed", TargetType: "user", IP: c.RealIP()})
			return c.AbortUnauthorized("invalid two-factor code")
		}
	}
	// Forced password change: the credentials (and 2FA) are valid, but the account
	// has an admin-set/reset password. Issue a short-lived reset session instead of
	// a real one — the user exchanges it for a session once they set their own
	// password, so they never have to re-enter the admin-set one.
	if user.MustChangePassword {
		token, err := h.auth.CreateResetSession(c.Request().Context(), user.ID)
		if err != nil {
			return c.AbortInternalServerError("failed to start password reset", err)
		}
		h.audit.Record(audit.Entry{ActorID: &user.ID, Action: "user.password_change_required", TargetType: "user", IP: c.RealIP()})
		return ok(c, AuthResponse{MustChangePassword: true, ResetToken: token})
	}
	now := time.Now()
	user.LastLoginAt = &now
	_ = h.users.Update(user)
	loginMeta := map[string]any{"via": "password"}
	if viaDirectory {
		loginMeta["via"] = "ldap"
	}
	h.audit.Record(audit.Entry{ActorID: &user.ID, Action: "user.login", TargetType: "user", IP: c.RealIP(), Metadata: loginMeta})
	return h.issue(c, user, 200)
}

// CompletePasswordResetRequest carries the reset token from login plus the new
// password the user chose.
type CompletePasswordResetRequest struct {
	Body struct {
		ResetToken  string `json:"reset_token" required:"true"`
		NewPassword string `json:"new_password" required:"true" minLength:"8"`
	} `json:"body"`
}

// CompletePasswordReset finishes the forced-change flow: it consumes the
// short-lived reset token issued at login, sets the new password (clearing the
// must-change flag), and issues a full session — so the user never re-enters the
// admin-set password.
func (h *AuthHandler) CompletePasswordReset(c *okapi.Context, req *CompletePasswordResetRequest) error {
	user, err := h.auth.CompletePasswordReset(c.Request().Context(), req.Body.ResetToken, req.Body.NewPassword)
	if err != nil {
		return c.AbortUnauthorized("this password-reset session has expired — sign in again")
	}
	now := time.Now()
	user.LastLoginAt = &now
	_ = h.users.Update(user)
	h.audit.Record(audit.Entry{ActorID: &user.ID, Action: "user.password_changed", TargetType: "user", IP: c.RealIP(), Metadata: map[string]any{"forced": true}})
	return h.issue(c, user, 200)
}

// LoginToken re-authenticates the user and mints a short-lived personal API
// token for the CLI ("Copy login command"). It never reads the session cookie —
// the token is issued only against credentials in this request, so a signed-in
// user re-authenticates (the OpenShift request-token property). No session is
// created; the console session is untouched.
func (h *AuthHandler) LoginToken(c *okapi.Context, req *LoginTokenRequest) error {
	if h.loginTokens == nil {
		return c.AbortWithError(503, errors.New("login tokens are not available"))
	}
	identifier := strings.TrimSpace(req.Body.Username)
	if identifier == "" {
		return c.AbortBadRequest("a username or email is required")
	}
	user, _, err := h.credentialCheck(c.Request().Context(), identifier, req.Body.Password)
	if err != nil {
		return h.abortCredential(c, err)
	}
	if user.TwoFactorEnabled {
		if req.Body.TwoFactorCode == "" {
			return ok(c, LoginTokenResponse{TwoFactorRequired: true})
		}
		if !h.auth.VerifyLoginCode(user, req.Body.TwoFactorCode) {
			h.audit.Record(audit.Entry{ActorID: &user.ID, Action: "user.login_2fa_failed", TargetType: "user", IP: c.RealIP()})
			return c.AbortUnauthorized("invalid two-factor code")
		}
	}
	if user.MustChangePassword {
		return c.AbortForbidden("set a new password before creating an API token")
	}
	tok, err := h.loginTokens.Issue(user.ID, req.Body.Scopes, req.Body.ExpiresInHours)
	if err != nil {
		if errors.Is(err, logintoken.ErrAdminScope) || errors.Is(err, logintoken.ErrInvalidScope) {
			return c.AbortBadRequest(err.Error())
		}
		return c.AbortInternalServerError("failed to issue login token", err)
	}
	h.audit.Record(audit.Entry{ActorID: &user.ID, Action: "user.login_token_issued", TargetType: "user", IP: c.RealIP(), Metadata: map[string]any{"scopes": tok.Scopes}})
	return ok(c, tokenPayload(tok))
}

// ClaimLoginTokenRequest carries the single-use hand-off reference minted by the
// SSO login-token flow (the OAuth callback stashed the token in Redis and
// redirected here with the reference, never the token itself).
type ClaimLoginTokenRequest struct {
	Body struct {
		Handoff string `json:"handoff" required:"true"`
	} `json:"body"`
}

// ClaimLoginToken exchanges a single-use hand-off reference for the login token
// the SSO flow minted. Public — the reference is the (short-lived, one-time)
// credential — but rate-limited like the other auth endpoints.
func (h *AuthHandler) ClaimLoginToken(c *okapi.Context, req *ClaimLoginTokenRequest) error {
	if h.loginTokens == nil {
		return c.AbortWithError(503, errors.New("login tokens are not available"))
	}
	tok, err := h.loginTokens.Claim(c.Request().Context(), req.Body.Handoff)
	if err != nil {
		return c.AbortNotFound("this login token is no longer available — request a new one")
	}
	return ok(c, tokenPayload(tok))
}

// issue generates a token, records the session, and returns the auth response.
func (h *AuthHandler) issue(c *okapi.Context, user *models.User, status int) error {
	token, jti, err := h.auth.IssueToken(user)
	if err != nil {
		return c.AbortInternalServerError("failed to issue token", err)
	}
	ua := c.Header("User-Agent")
	if len(ua) > 512 {
		ua = ua[:512]
	}
	if err := h.sessions.Create(&models.Session{
		UserID: user.ID, JTI: jti, IPAddress: c.RealIP(), UserAgent: ua,
		ExpiresAt: time.Now().Add(auth.TokenTTL),
	}); err != nil {
		logger.Error("failed to record session", "error", err)
	}
	setSessionCookie(c, token)
	profile := profileOf(user)
	resp := AuthResponse{Token: token, User: &profile}
	if status == 201 {
		return created(c, resp)
	}
	return ok(c, resp)
}

// Logout revokes the current JWT session.
func (h *AuthHandler) Logout(c *okapi.Context) error {
	jti := c.GetString("jti")
	if jti != "" {
		h.auth.Revoke(c.Request().Context(), jti)
		_ = h.sessions.RevokeByJTI(jti)
	}
	clearSessionCookie(c)
	return message(c, "logged out")
}

// Me returns the authenticated user's profile.
func (h *AuthHandler) Me(c *okapi.Context) error {
	user, err := h.users.FindByID(middlewares.UserID(c))
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	profile := profileOf(user)
	if user.TwoFactorEnabled {
		n := h.auth.RecoveryCodesRemaining(user.ID)
		profile.RecoveryCodesRemaining = &n
	}
	profile.Auth = &AuthContext{Method: middlewares.AuthMethod(c)}
	if middlewares.AuthMethod(c) == "api_key" {
		id := middlewares.APIKeyID(c)
		profile.Auth.APIKeyID = &id
		profile.Auth.WorkspaceID = middlewares.APIKeyWorkspaceID(c)
		profile.Auth.Scopes = middlewares.APIKeyScopes(c)
	}
	return ok(c, profile)
}

// ForgotPassword issues a reset token and emails the reset link. Always returns
// 200 to avoid leaking whether the email exists. Self-service password reset is
// gated by MIABI_PASSWORD_RESET_ENABLED (a boot-time control): when disabled, no
// token is created and no email is sent.
func (h *AuthHandler) ForgotPassword(c *okapi.Context, req *ForgotPasswordRequest) error {
	if !h.passwordResetEnabled {
		return message(c, "if the email exists, a reset link has been sent")
	}
	raw, user, err := h.auth.CreatePasswordReset(req.Body.Email)
	if err != nil {
		return c.AbortInternalServerError("failed to process request", err)
	}
	if user != nil {
		// Deliver the reset link by email (best-effort, async). The token is a full
		// account-takeover credential, so it is never logged in production; in dev
		// it is surfaced so the flow stays testable without an SMTP server.
		h.mailer.SendPasswordReset(user.Email, user.Name, raw, int(auth.PasswordResetTTL/time.Hour))
		if h.devMode {
			logger.Warn("password reset token (dev only — not logged in production)", "user_id", user.ID, "token", raw)
		} else {
			logger.Info("password reset requested", "user_id", user.ID)
		}
		h.audit.Record(audit.Entry{ActorID: &user.ID, Action: "user.password_reset_requested", TargetType: "user", IP: c.RealIP()})
	}
	return message(c, "if the email exists, a reset link has been sent")
}

// ResetPassword consumes a reset token and sets a new password.
func (h *AuthHandler) ResetPassword(c *okapi.Context, req *ResetPasswordRequest) error {
	if err := h.auth.ResetPassword(req.Body.Token, req.Body.Password); err != nil {
		return c.AbortBadRequest("invalid or expired reset token")
	}
	return message(c, "password updated")
}

// ChangePassword lets an authenticated user change their own password by
// supplying their current one (the self-service path from the security page).
func (h *AuthHandler) ChangePassword(c *okapi.Context, req *ChangePasswordRequest) error {
	uid := middlewares.UserID(c)
	if err := h.auth.ChangePassword(uid, req.Body.CurrentPassword, req.Body.NewPassword); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			return c.AbortBadRequest("current password is incorrect")
		}
		return c.AbortInternalServerError("failed to change password", err)
	}
	h.audit.Record(audit.Entry{ActorID: &uid, Action: "user.password_changed", TargetType: "user", IP: c.RealIP()})
	return message(c, "password changed")
}

// UpdateProfile lets an authenticated user edit their own profile — the display
// name and (optionally) the username handle. Email and role are managed by an
// admin, not self-service.
func (h *AuthHandler) UpdateProfile(c *okapi.Context, req *UpdateProfileRequest) error {
	uid := middlewares.UserID(c)
	user, err := h.users.FindByID(uid)
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	name := strings.TrimSpace(req.Body.Name)
	if name == "" {
		return c.AbortBadRequest("name cannot be empty")
	}
	username, err := validateUsername(h.users, req.Body.Username, uid)
	if err != nil {
		if errors.Is(err, errUsernameTaken) {
			return c.AbortWithError(409, err)
		}
		return c.AbortBadRequest(err.Error())
	}
	user.Name = name
	if username != "" {
		user.Username = username
	}
	if err := h.users.Update(user); err != nil {
		return c.AbortInternalServerError("failed to update profile", err)
	}
	h.audit.Record(audit.Entry{ActorID: &uid, Action: "user.profile_updated", TargetType: "user", IP: c.RealIP()})
	return ok(c, profileOf(user))
}

// DismissOnboarding marks the getting-started checklist as dismissed (or
// completed) for the authenticated user, so the web stops showing it. Idempotent.
func (h *AuthHandler) DismissOnboarding(c *okapi.Context) error {
	user, err := h.users.FindByID(middlewares.UserID(c))
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if user.OnboardingDismissedAt == nil {
		now := time.Now()
		user.OnboardingDismissedAt = &now
		if err := h.users.Update(user); err != nil {
			return c.AbortInternalServerError("failed to update onboarding state", err)
		}
	}
	return ok(c, profileOf(user))
}

// ListSessions returns the authenticated user's active sign-in sessions,
// flagging the one the current request is using so the UI can label it and
// prevent revoking the session you're sitting on.
func (h *AuthHandler) ListSessions(c *okapi.Context) error {
	uid := middlewares.UserID(c)
	sessions, err := h.sessions.ListByUser(uid)
	if err != nil {
		return c.AbortInternalServerError("failed to list sessions", err)
	}
	currentJTI := c.GetString("jti")
	out := make([]SessionInfo, 0, len(sessions))
	for i := range sessions {
		s := sessions[i]
		if !s.IsActive() {
			continue
		}
		out = append(out, SessionInfo{
			ID:        s.ID,
			IPAddress: s.IPAddress,
			UserAgent: s.UserAgent,
			Current:   s.JTI == currentJTI,
			CreatedAt: s.CreatedAt,
			ExpiresAt: s.ExpiresAt,
		})
	}
	return ok(c, out)
}

// RevokeSession revokes one of the authenticated user's own sessions, signing
// out that device. The session is blacklisted (Redis) and marked revoked so the
// JWT is rejected on its next request.
func (h *AuthHandler) RevokeSession(c *okapi.Context) error {
	uid := middlewares.UserID(c)
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return c.AbortBadRequest("invalid session id")
	}
	s, err := h.sessions.FindByIDForUser(uint(id), uid)
	if err != nil {
		return c.AbortNotFound("session not found")
	}
	h.auth.Revoke(c.Request().Context(), s.JTI)
	_ = h.sessions.RevokeByJTI(s.JTI)
	h.audit.Record(audit.Entry{ActorID: &uid, Action: "user.session_revoked", TargetType: "user", IP: c.RealIP()})
	return message(c, "session revoked")
}

// RevokeOtherSessions signs out every active session belonging to the user
// except the one making this request, so a user can drop all other devices in
// one click without logging themselves out.
func (h *AuthHandler) RevokeOtherSessions(c *okapi.Context) error {
	uid := middlewares.UserID(c)
	currentJTI := c.GetString("jti")
	sessions, err := h.sessions.ListByUser(uid)
	if err != nil {
		return c.AbortInternalServerError("failed to list sessions", err)
	}
	ctx := c.Request().Context()
	revoked := 0
	for i := range sessions {
		s := sessions[i]
		if s.JTI == currentJTI || !s.IsActive() {
			continue
		}
		h.auth.Revoke(ctx, s.JTI)
		_ = h.sessions.RevokeByJTI(s.JTI)
		revoked++
	}
	if revoked > 0 {
		h.audit.Record(audit.Entry{ActorID: &uid, Action: "user.sessions_revoked_others", TargetType: "user", IP: c.RealIP()})
	}
	return ok(c, RevokeOthersResult{Revoked: revoked, Message: fmt.Sprintf("signed out %d other session(s)", revoked)})
}

// Setup2FA generates a pending TOTP secret for the authenticated user and
// returns it with an otpauth:// URL for QR rendering. 2FA is not active until
// confirmed via Verify2FA.
func (h *AuthHandler) Setup2FA(c *okapi.Context) error {
	user, err := h.users.FindByID(middlewares.UserID(c))
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	secret, url, err := h.auth.BeginTwoFactorSetup(user)
	if err != nil {
		if errors.Is(err, auth.ErrTwoFactorAlreadyEnabled) {
			return c.AbortBadRequest("two-factor authentication is already enabled")
		}
		return c.AbortInternalServerError("failed to start two-factor setup", err)
	}
	qr, err := twofactor.QRDataURI(url, 220)
	if err != nil {
		return c.AbortInternalServerError("failed to render QR code", err)
	}
	return ok(c, Setup2FAResponse{Secret: secret, URL: url, QRCode: qr})
}

// Verify2FA confirms a TOTP code, activates two-factor authentication, and
// returns the user's one-time recovery codes (shown once).
func (h *AuthHandler) Verify2FA(c *okapi.Context, req *Verify2FARequest) error {
	user, err := h.users.FindByID(middlewares.UserID(c))
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	codes, err := h.auth.ConfirmTwoFactor(user, req.Body.Code)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrTwoFactorAlreadyEnabled):
			return c.AbortBadRequest("two-factor authentication is already enabled")
		case errors.Is(err, auth.ErrTwoFactorNotInitiated):
			return c.AbortBadRequest("two-factor setup has not been initiated")
		case errors.Is(err, auth.ErrInvalidTwoFactorCode):
			return c.AbortBadRequest("invalid two-factor code")
		default:
			return c.AbortInternalServerError("failed to enable two-factor authentication", err)
		}
	}
	h.audit.Record(audit.Entry{ActorID: &user.ID, Action: "user.2fa_enabled", TargetType: "user", IP: c.RealIP()})
	return ok(c, RecoveryCodesResponse{RecoveryCodes: codes})
}

// RegenerateRecoveryCodes verifies a current TOTP code and replaces the user's
// recovery codes with a fresh set.
func (h *AuthHandler) RegenerateRecoveryCodes(c *okapi.Context, req *RegenerateCodesRequest) error {
	user, err := h.users.FindByID(middlewares.UserID(c))
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	codes, err := h.auth.RegenerateRecoveryCodes(user, req.Body.Code)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrTwoFactorNotEnabled):
			return c.AbortBadRequest("two-factor authentication is not enabled")
		case errors.Is(err, auth.ErrInvalidTwoFactorCode):
			return c.AbortBadRequest("invalid two-factor code")
		default:
			return c.AbortInternalServerError("failed to regenerate recovery codes", err)
		}
	}
	h.audit.Record(audit.Entry{ActorID: &user.ID, Action: "user.2fa_recovery_regenerated", TargetType: "user", IP: c.RealIP()})
	return ok(c, RecoveryCodesResponse{RecoveryCodes: codes})
}

// Disable2FA turns off two-factor authentication after verifying a code.
func (h *AuthHandler) Disable2FA(c *okapi.Context, req *Disable2FARequest) error {
	user, err := h.users.FindByID(middlewares.UserID(c))
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if err := h.auth.DisableTwoFactor(user, req.Body.Code); err != nil {
		switch {
		case errors.Is(err, auth.ErrTwoFactorNotEnabled):
			return c.AbortBadRequest("two-factor authentication is not enabled")
		case errors.Is(err, auth.ErrInvalidTwoFactorCode):
			return c.AbortBadRequest("invalid two-factor code")
		default:
			return c.AbortInternalServerError("failed to disable two-factor authentication", err)
		}
	}
	h.audit.Record(audit.Entry{ActorID: &user.ID, Action: "user.2fa_disabled", TargetType: "user", IP: c.RealIP()})
	return message(c, "two-factor authentication disabled")
}
