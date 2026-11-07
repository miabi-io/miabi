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
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/auth"
	"github.com/miabi-io/miabi/internal/services/branding"
	"github.com/miabi-io/miabi/internal/services/directory"
	"github.com/miabi-io/miabi/internal/services/logintoken"
	"github.com/miabi-io/miabi/internal/services/mailer"
	"github.com/miabi-io/miabi/internal/services/passkey"
	"github.com/miabi-io/miabi/internal/services/registration"
	"github.com/miabi-io/miabi/internal/services/settings"
	"github.com/miabi-io/miabi/internal/services/twofactor"
	"github.com/miabi-io/miabi/internal/services/usersettings"
	"github.com/miabi-io/miabi/internal/services/workspace"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

type AuthHandler struct {
	auth         *auth.Service
	userSettings *usersettings.Service
	users        *repositories.UserRepository
	sessions     *repositories.SessionRepository
	audit        *audit.Logger
	settings     *settings.Provider
	mailer       *mailer.Service
	devMode      bool
	// A critical auth control, so fixed at boot from MIABI_PASSWORD_RESET_ENABLED, not a runtime setting.
	passwordResetEnabled bool
	branding             *branding.Service
	ee                   enterprise.EE
	// registration decides whether the sign-in page offers a sign-up link.
	registration   *registration.Service
	creationPolicy func(userID uint) workspace.CreationPolicy
	// Platform admins are exempt so a misconfigured IdP can't lock everyone out.
	enforceSSO func(user *models.User) bool
	// Returns (user,nil) on success, (nil,nil) to fall through, (nil,err) on a bind/disabled failure.
	directoryLogin func(ctx context.Context, identifier, password string) (*models.User, error)
	loginTokens    *logintoken.Service
	// passkeys is nil when the server has no public URL to bind them to. Set by SetPasskeys.
	passkeys    *passkey.Service
	passkeyRepo *repositories.PasskeyRepository
}

func (h *AuthHandler) SetLoginTokens(s *logintoken.Service) { h.loginTokens = s }

// SetSSOEnforcement wires the enforced-SSO predicate (Enterprise). Optional.
func (h *AuthHandler) SetSSOEnforcement(fn func(user *models.User) bool) {
	h.enforceSSO = fn
}

func (h *AuthHandler) SetDirectoryLogin(fn func(ctx context.Context, identifier, password string) (*models.User, error)) {
	h.directoryLogin = fn
}

func (h *AuthHandler) SetMailer(m *mailer.Service) { h.mailer = m }

func (h *AuthHandler) SetBranding(b *branding.Service, ee enterprise.EE) {
	h.branding, h.ee = b, ee
}

// SetRegistration wires the sign-up policy (nil-safe: unset reports closed).
func (h *AuthHandler) SetRegistration(r *registration.Service) { h.registration = r }

// SetCreationPolicy wires the workspace creation policy reported on /me (nil-safe: omitted).
func (h *AuthHandler) SetCreationPolicy(p func(userID uint) workspace.CreationPolicy) {
	h.creationPolicy = p
}

func NewAuthHandler(a *auth.Service, users *repositories.UserRepository, sessions *repositories.SessionRepository, auditLog *audit.Logger, settingsProvider *settings.Provider, devMode, passwordResetEnabled bool) *AuthHandler {
	return &AuthHandler{auth: a, users: users, sessions: sessions, audit: auditLog, settings: settingsProvider, devMode: devMode, passwordResetEnabled: passwordResetEnabled}
}

func (h *AuthHandler) SetUserSettings(s *usersettings.Service) { h.userSettings = s }

type LoginRequest struct {
	Body struct {
		// Username is the login handle — a username or an email address.
		Username      string `json:"username" required:"true"`
		Password      string `json:"password" required:"true"`
		TwoFactorCode string `json:"two_factor_code"`
	} `json:"body"`
}

// Takes credentials in the body: the token is never issued off an ambient session.
type LoginTokenRequest struct {
	Body struct {
		Username      string `json:"username" required:"true"`
		Password      string `json:"password" required:"true"`
		TwoFactorCode string `json:"two_factor_code"`
		// Switches to the loopback flow; must be a loopback URI, and State is echoed as the CLI's CSRF check.
		RedirectURI string `json:"redirect_uri"`
		State       string `json:"state"`
		// admin and "*" are rejected; empty defaults to read/write/deploy.
		Scopes []string `json:"scopes"`
		// ExpiresInHours optionally overrides the default lifetime, capped server-side.
		ExpiresInHours *int `json:"expires_in_hours"`
	} `json:"body"`
}

type LoginTokenResponse struct {
	Token             string     `json:"token,omitempty"`
	SHA256            string     `json:"sha256,omitempty"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
	Scopes            []string   `json:"scopes,omitempty"`
	ServerURL         string     `json:"server_url,omitempty"`
	LoginCommand      string     `json:"login_command,omitempty"`
	CurlExample       string     `json:"curl_example,omitempty"`
	TwoFactorRequired bool       `json:"two_factor_required,omitempty"`
	// Set only for the loopback flow, in which case the raw token is omitted.
	RedirectTo string `json:"redirect_to,omitempty"`
}

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
		Password string `json:"password" required:"true" minLength:"12"`
	} `json:"body"`
}

type ChangePasswordRequest struct {
	Body struct {
		CurrentPassword string `json:"current_password" required:"true"`
		NewPassword     string `json:"new_password" required:"true" minLength:"12"`
	} `json:"body"`
}

// UpdateProfileRequest carries the editable fields of a user's own profile.
type UpdateProfileRequest struct {
	Body struct {
		Name     string `json:"name" required:"true" minLength:"1" maxLength:"100"`
		Username string `json:"username"`
	} `json:"body"`
}

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
	ID                  uint              `json:"id"`
	Name                string            `json:"name"`
	Username            string            `json:"username"`
	Email               string            `json:"email"`
	Role                models.SystemRole `json:"role"`
	TwoFactorEnabled    bool              `json:"two_factor_enabled"`
	OnboardingDismissed bool              `json:"onboarding_dismissed"`
	AuthSource          string            `json:"auth_source"`
	ProfileManaged      bool              `json:"profile_managed"`
	// RecoveryCodesRemaining is populated on the profile (/me) endpoint only.
	RecoveryCodesRemaining *int         `json:"recovery_codes_remaining,omitempty"`
	Auth                   *AuthContext `json:"auth,omitempty"`
	// A landing hint only: authorization still comes from WorkspaceScope on every request.
	DefaultWorkspaceID *uint `json:"default_workspace_id,omitempty"`
	// Preferences are the user's console settings. Populated on /me only.
	Preferences       *models.UserSetting       `json:"preferences,omitempty"`
	WorkspaceCreation *workspace.CreationPolicy `json:"workspace_creation,omitempty"`
}

type AuthContext struct {
	Method      string   `json:"method"`                 // "jwt" | "api_key"
	APIKeyID    *uint    `json:"api_key_id,omitempty"`   // nil for session callers
	WorkspaceID *uint    `json:"workspace_id,omitempty"` // the key's bound workspace
	Scopes      []string `json:"scopes,omitempty"`
}

type AuthResponse struct {
	Token             string       `json:"token,omitempty"`
	User              *UserProfile `json:"user,omitempty"`
	TwoFactorRequired bool         `json:"two_factor_required,omitempty"`
	// No session is issued; ResetToken is single-use and exchanged via /auth/complete-password-reset.
	MustChangePassword bool   `json:"must_change_password,omitempty"`
	ResetToken         string `json:"reset_token,omitempty"`
}

type Setup2FAResponse struct {
	Secret string `json:"secret"`
	URL    string `json:"url"`
	QRCode string `json:"qr_code"`
}

type RecoveryCodesResponse struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

type Verify2FARequest struct {
	Body struct {
		Code string `json:"code" required:"true" minLength:"6" maxLength:"6"`
	} `json:"body"`
}

// Accepts a TOTP or a recovery code, so the field is not length-constrained.
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

type AuthStatus struct {
	PasswordResetEnabled bool `json:"password_reset_enabled"`
	// The sign-in page has no user and so no personal preference; empty fields mean Miabi's own.
	Brand branding.Branding `json:"brand"`
	// False also when sign-up is on but unusable; see registration.Available.
	RegistrationEnabled bool `json:"registration_enabled"`
	// PasskeysEnabled tells the sign-in page whether to offer passkey sign-in.
	PasskeysEnabled bool `json:"passkeys_enabled"`
}

func profileOf(u *models.User) UserProfile {
	src := u.AuthSource
	if strings.TrimSpace(src) == "" {
		src = models.AuthSourceLocal
	}
	return UserProfile{ID: u.ID, Name: u.Name, Username: u.Username, Email: u.Email, Role: u.Role, TwoFactorEnabled: u.TwoFactorEnabled, OnboardingDismissed: u.OnboardingDismissedAt != nil, AuthSource: src, ProfileManaged: u.IsExternal()}
}

func authSourceLabel(src string) string {
	switch strings.ToLower(strings.TrimSpace(src)) {
	case models.AuthSourceLDAP:
		return "your directory (LDAP)"
	case models.AuthSourceOAuth:
		return "your single sign-on provider"
	case models.AuthSourceSAML:
		return "your single sign-on provider (SAML)"
	case models.AuthSourceSCIM:
		return "your identity provider (SCIM)"
	default:
		return "your identity provider"
	}
}

func (h *AuthHandler) Status(c *okapi.Context) error {
	st := AuthStatus{PasswordResetEnabled: h.passwordResetEnabled, PasskeysEnabled: h.passkeys.Enabled()}
	if h.registration != nil {
		st.RegistrationEnabled = h.registration.Available() == nil
	}
	// Read, not write: an expired licence must not replace an operator's sign-in branding.
	if h.branding != nil && h.ee != nil && h.ee.Has(enterprise.FlagWhiteLabel) {
		st.Brand = h.branding.Public()
	}
	return ok(c, st)
}

var (
	errEmailUnverified = errors.New("email address is not verified")
	errSSORequired     = errors.New("password login is disabled for this organization; use single sign-on")
)

// Returns the user and whether auth came via the directory; 2FA and success are per-caller.
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
	// Platform admins are exempt as a lock-out safety valve; a directory login is itself an SSO method.
	if !viaDirectory && h.enforceSSO != nil && h.enforceSSO(user) {
		return nil, false, errSSORequired
	}
	return user, viaDirectory, nil
}

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
	if user.TwoFactorEnabled {
		if req.Body.TwoFactorCode == "" {
			return ok(c, AuthResponse{TwoFactorRequired: true})
		}
		if !h.auth.VerifyLoginCode(user, req.Body.TwoFactorCode) {
			h.audit.Record(audit.Entry{ActorID: &user.ID, Action: "user.login_2fa_failed", TargetType: "user", IP: c.RealIP()})
			return c.AbortUnauthorized("invalid two-factor code")
		}
	}
	// Issue a short-lived reset session so the user never has to re-enter the admin-set password.
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

type CompletePasswordResetRequest struct {
	Body struct {
		ResetToken  string `json:"reset_token" required:"true"`
		NewPassword string `json:"new_password" required:"true" minLength:"12"`
	} `json:"body"`
}

func (h *AuthHandler) CompletePasswordReset(c *okapi.Context, req *CompletePasswordResetRequest) error {
	user, err := h.auth.CompletePasswordReset(c.Request().Context(), req.Body.ResetToken, req.Body.NewPassword)
	if errors.Is(err, models.ErrPasswordTooShort) {
		return c.AbortBadRequest(err.Error())
	}
	if err != nil {
		return c.AbortUnauthorized("this password-reset session has expired — sign in again")
	}
	now := time.Now()
	user.LastLoginAt = &now
	_ = h.users.Update(user)
	h.audit.Record(audit.Entry{ActorID: &user.ID, Action: "user.password_changed", TargetType: "user", IP: c.RealIP(), Metadata: map[string]any{"forced": true}})
	h.revokeSessions(c, user.ID)
	return h.issue(c, user, 200)
}

// LoginToken never reads the session cookie: it is issued only against credentials in this request.
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
	// A token must never be handed to an arbitrary redirect target.
	redirectURI := strings.TrimSpace(req.Body.RedirectURI)
	if redirectURI != "" && !isLoopbackRedirect(redirectURI) {
		return c.AbortBadRequest("redirect_uri must be a loopback (127.0.0.1/localhost) address")
	}
	tok, err := h.loginTokens.Issue(user.ID, req.Body.Scopes, req.Body.ExpiresInHours)
	if err != nil {
		if errors.Is(err, logintoken.ErrAdminScope) || errors.Is(err, logintoken.ErrInvalidScope) {
			return c.AbortBadRequest(err.Error())
		}
		return c.AbortInternalServerError("failed to issue login token", err)
	}
	h.audit.Record(audit.Entry{ActorID: &user.ID, Action: "user.login_token_issued", TargetType: "user", IP: c.RealIP(), Metadata: map[string]any{"scopes": tok.Scopes, "via": redirectVia(redirectURI)}})
	// No loopback target: return the raw token for the "Copy login command" page.
	if redirectURI == "" {
		return ok(c, tokenPayload(tok))
	}
	// The secret rides a one-time reference, not the address bar.
	ref, err := h.loginTokens.Stash(c.Request().Context(), tok)
	if err != nil {
		return c.AbortInternalServerError("failed to prepare CLI login", err)
	}
	return ok(c, LoginTokenResponse{RedirectTo: buildCliRedirect(redirectURI, ref, req.Body.State)})
}

// AuthorizeCLIRequest asks to sign `miabi login` in with the browser's current session.
type AuthorizeCLIRequest struct {
	Body struct {
		// RedirectURI is the CLI's loopback callback; State is echoed back as its CSRF check.
		RedirectURI string   `json:"redirect_uri" required:"true"`
		State       string   `json:"state"`
		Scopes      []string `json:"scopes"`
	} `json:"body"`
}

// Session-only: an API key must not be able to mint itself more tokens.
func (h *AuthHandler) AuthorizeCLI(c *okapi.Context, req *AuthorizeCLIRequest) error {
	if h.loginTokens == nil {
		return c.AbortWithError(503, errors.New("login tokens are not available"))
	}
	redirectURI := strings.TrimSpace(req.Body.RedirectURI)
	if !isLoopbackRedirect(redirectURI) {
		return c.AbortBadRequest("redirect_uri must be a loopback (127.0.0.1/localhost) address")
	}
	user, err := h.users.FindByID(middlewares.UserID(c))
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if user.MustChangePassword {
		return c.AbortForbidden("set a new password before creating an API token")
	}
	tok, err := h.loginTokens.Issue(user.ID, req.Body.Scopes, nil)
	if err != nil {
		if errors.Is(err, logintoken.ErrAdminScope) || errors.Is(err, logintoken.ErrInvalidScope) {
			return c.AbortBadRequest(err.Error())
		}
		return c.AbortInternalServerError("failed to issue login token", err)
	}
	h.audit.Record(audit.Entry{ActorID: &user.ID, Action: "user.login_token_issued", TargetType: "user", IP: c.RealIP(),
		Metadata: map[string]any{"scopes": tok.Scopes, "via": "cli_session"}})
	ref, err := h.loginTokens.Stash(c.Request().Context(), tok)
	if err != nil {
		return c.AbortInternalServerError("failed to prepare CLI login", err)
	}
	return ok(c, LoginTokenResponse{RedirectTo: buildCliRedirect(redirectURI, ref, req.Body.State)})
}

func isLoopbackRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return false
	}
	switch u.Hostname() {
	case "127.0.0.1", "::1", "localhost":
		return true
	default:
		return false
	}
}

func buildCliRedirect(redirectURI, code, state string) string {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return redirectURI
	}
	q := u.Query()
	q.Set("code", code)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func redirectVia(redirectURI string) string {
	if redirectURI == "" {
		return "display"
	}
	return "cli_loopback"
}

// The OAuth callback stashes the token in Redis and redirects with the reference, never the token.
type ClaimLoginTokenRequest struct {
	Body struct {
		Handoff string `json:"handoff" required:"true"`
	} `json:"body"`
}

// Public because the one-time reference is the credential, but rate-limited like other auth endpoints.
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
	if h.userSettings != nil {
		if ws, err := h.userSettings.DefaultWorkspace(user.ID); err == nil && ws != nil {
			id := ws.ID
			profile.DefaultWorkspaceID = &id
		}
		if prefs, err := h.userSettings.Get(user.ID); err == nil {
			profile.Preferences = prefs
		}
	}
	if h.creationPolicy != nil {
		p := h.creationPolicy(user.ID)
		profile.WorkspaceCreation = &p
	}
	return ok(c, profile)
}

type UpdatePreferencesRequest struct {
	Body struct {
		Theme       *string `json:"theme,omitempty"`
		Timezone    *string `json:"timezone,omitempty" format:"timezone" minLength:"1" example:"Africa/Kinshasa" description:"IANA time zone name"`
		Accent      *string `json:"accent,omitempty"`
		Locale      *string `json:"locale,omitempty"`
		LandingView *string `json:"landing_view,omitempty"`
	} `json:"body"`
}

// UpdatePreferences saves the caller's console preferences.
func (h *AuthHandler) UpdatePreferences(c *okapi.Context, req *UpdatePreferencesRequest) error {
	if h.userSettings == nil {
		return c.AbortInternalServerError("preferences are unavailable", nil)
	}
	out, err := h.userSettings.Save(middlewares.UserID(c), usersettings.Update{
		Theme: req.Body.Theme, Timezone: req.Body.Timezone,
		Accent: req.Body.Accent, Locale: req.Body.Locale, LandingView: req.Body.LandingView,
	})
	if err != nil {
		if errors.Is(err, usersettings.ErrAccentLocked) {
			return c.AbortForbidden(err.Error())
		}
		if errors.Is(err, usersettings.ErrInvalidTheme) || errors.Is(err, usersettings.ErrInvalidAccent) ||
			errors.Is(err, usersettings.ErrInvalidLocale) || errors.Is(err, usersettings.ErrInvalidLandingView) ||
			errors.Is(err, usersettings.ErrInvalidTimezone) {
			return c.AbortBadRequest(err.Error())
		}
		return c.AbortInternalServerError("failed to save preferences", err)
	}
	return ok(c, out)
}

type SetDefaultWorkspaceRequest struct {
	Body struct {
		WorkspaceID *uint `json:"workspace_id"`
	} `json:"body"`
}

// SetDefaultWorkspace records the caller's landing workspace.
func (h *AuthHandler) SetDefaultWorkspace(c *okapi.Context, req *SetDefaultWorkspaceRequest) error {
	if h.userSettings == nil {
		return c.AbortInternalServerError("preferences are unavailable", nil)
	}
	userID := middlewares.UserID(c)
	if err := h.userSettings.SetDefaultWorkspace(userID, req.Body.WorkspaceID); err != nil {
		if errors.Is(err, usersettings.ErrNotMember) {
			return c.AbortForbidden(err.Error())
		}
		return c.AbortInternalServerError("failed to set the default workspace", err)
	}
	ws, err := h.userSettings.DefaultWorkspace(userID)
	if err != nil {
		return c.AbortInternalServerError("failed to resolve the default workspace", err)
	}
	out := SetDefaultWorkspaceRequest{}.Body
	if ws != nil {
		id := ws.ID
		out.WorkspaceID = &id
	}
	return ok(c, out)
}

// Always returns 200 so it cannot leak whether the email exists.
func (h *AuthHandler) ForgotPassword(c *okapi.Context, req *ForgotPasswordRequest) error {
	if !h.passwordResetEnabled {
		return message(c, "if the email exists, a reset link has been sent")
	}
	raw, user, err := h.auth.CreatePasswordReset(req.Body.Email)
	if err != nil {
		return c.AbortInternalServerError("failed to process request", err)
	}
	if user != nil {
		// The token is an account-takeover credential: never logged in production, surfaced only in dev.
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
	uid, err := h.auth.ResetPassword(req.Body.Token, req.Body.Password)
	if errors.Is(err, models.ErrPasswordTooShort) {
		return c.AbortBadRequest(err.Error())
	}
	if err != nil {
		return c.AbortBadRequest("invalid or expired reset token")
	}
	h.revokeSessions(c, uid)
	return message(c, "password updated")
}

func (h *AuthHandler) ChangePassword(c *okapi.Context, req *ChangePasswordRequest) error {
	uid := middlewares.UserID(c)
	if err := h.auth.ChangePassword(uid, req.Body.CurrentPassword, req.Body.NewPassword); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			return c.AbortBadRequest("current password is incorrect")
		}
		if errors.Is(err, models.ErrPasswordTooShort) {
			return c.AbortBadRequest(err.Error())
		}
		return c.AbortInternalServerError("failed to change password", err)
	}
	h.audit.Record(audit.Entry{ActorID: &uid, Action: "user.password_changed", TargetType: "user", IP: c.RealIP()})
	// A fresh session keeps the user signed in here while everywhere else is signed out.
	h.revokeSessions(c, uid)
	user, err := h.users.FindByID(uid)
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	return h.issue(c, user, 200)
}

// Keeps the sessions list consistent with what the middleware already enforces after a password change.
func (h *AuthHandler) revokeSessions(c *okapi.Context, userID uint) {
	sessions, err := h.sessions.ListByUser(userID)
	if err != nil {
		return
	}
	ctx := c.Request().Context()
	for _, s := range sessions {
		if s.Revoked || time.Now().After(s.ExpiresAt) {
			continue
		}
		h.auth.Revoke(ctx, s.JTI)
	}
	_ = h.sessions.RevokeAllForUser(userID, "")
}

// Email and role are admin-managed; an externally provisioned account is managed by its IdP.
func (h *AuthHandler) UpdateProfile(c *okapi.Context, req *UpdateProfileRequest) error {
	uid := middlewares.UserID(c)
	user, err := h.users.FindByID(uid)
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if user.IsExternal() {
		return c.AbortForbidden("your profile is managed by " + authSourceLabel(user.AuthSource) + "; update it there")
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

// 2FA is not active until confirmed via Verify2FA.
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
