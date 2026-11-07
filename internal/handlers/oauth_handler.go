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
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/auth"
	"github.com/miabi-io/miabi/internal/services/logintoken"
	"github.com/miabi-io/miabi/internal/services/oauth"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// OAuthHandler serves the public SSO login flow (unauthenticated).
type OAuthHandler struct {
	oauth       *oauth.Service
	repo        *repositories.OAuthProviderRepository
	auth        *auth.Service
	sessions    *repositories.SessionRepository
	audit       *audit.Logger
	cfg         *config.Config
	loginTokens *logintoken.Service
}

func NewOAuthHandler(oauthSvc *oauth.Service, repo *repositories.OAuthProviderRepository, authSvc *auth.Service, sessions *repositories.SessionRepository, auditLog *audit.Logger, cfg *config.Config) *OAuthHandler {
	return &OAuthHandler{oauth: oauthSvc, repo: repo, auth: authSvc, sessions: sessions, audit: auditLog, cfg: cfg}
}

func (h *OAuthHandler) SetLoginTokens(s *logintoken.Service) { h.loginTokens = s }

const intentLoginToken = "login_token"

// The loopback target, CLI state and narrowed scopes ride in the intent value, joined by intentSep.
const intentCliLogin = "cli_login"

// Lets a deep link such as "Open in Miabi" survive the IdP round-trip.
const intentNext = "next"

// A unit separator never appears in a URL or state.
const intentSep = "\x1f"

// PublicProvider is the safe subset of a provider exposed on the login screen.
type PublicProvider struct {
	Name        string `json:"name"`         // unique handle
	DisplayName string `json:"display_name"` // login-button label
	Type        string `json:"type"`
}

type ProvidersResponse struct {
	Providers    []PublicProvider `json:"providers"`
	SSOAvailable bool             `json:"sso_available"`
}

func (h *OAuthHandler) ListProviders(c *okapi.Context) error {
	providers, err := h.repo.FindEnabled()
	if err != nil {
		return c.AbortInternalServerError("failed to list providers", err)
	}
	out := make([]PublicProvider, 0, len(providers))
	for _, p := range providers {
		out = append(out, PublicProvider{Name: p.Name, DisplayName: p.DisplayName, Type: string(p.Type)})
	}
	ssoAvailable, _ := h.repo.HasEnabledHidden()
	return ok(c, ProvidersResponse{Providers: out, SSOAvailable: ssoAvailable})
}

// DiscoverSSORequest carries the email whose domain is matched to a provider.
type DiscoverSSORequest struct {
	Body struct {
		Email string `json:"email" required:"true" format:"email"`
	} `json:"body"`
}

type DiscoverSSOResponse struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
}

func (h *OAuthHandler) DiscoverSSO(c *okapi.Context, req *DiscoverSSORequest) error {
	email := strings.TrimSpace(req.Body.Email)
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return c.AbortBadRequest("enter a valid email address")
	}
	p, err := h.repo.FindEnabledByDomain(email[at+1:])
	if err != nil {
		return c.AbortNotFound("no single sign-on provider is configured for this email")
	}
	return ok(c, DiscoverSSOResponse{Name: p.Name, DisplayName: p.DisplayName, Type: string(p.Type)})
}

// login_token forces a fresh IdP login (prompt=login) and the callback mints a CLI token.
func (h *OAuthHandler) Authorize(c *okapi.Context) error {
	p, err := h.oauth.Get(c.Param("slug"))
	if err != nil {
		return c.AbortNotFound("provider not found")
	}
	intent := ""
	switch c.Query("intent") {
	case intentLoginToken:
		if h.loginTokens != nil {
			intent = intentLoginToken
			if sc := strings.TrimSpace(c.Query("cli_scopes")); sc != "" {
				intent += intentSep + sc
			}
		}
	case intentCliLogin:
		// Reject non-loopback targets before ever starting the flow.
		cliRedirect := strings.TrimSpace(c.Query("cli_redirect"))
		if h.loginTokens != nil && isLoopbackRedirect(cliRedirect) {
			intent = strings.Join([]string{
				intentCliLogin, cliRedirect,
				strings.TrimSpace(c.Query("cli_state")),
				strings.TrimSpace(c.Query("cli_scopes")),
			}, intentSep)
		}
	default:
		if next := SafeNext(c.Query("next")); next != "" {
			intent = intentNext + intentSep + next
		}
	}
	redirectURI := h.callbackURI(c, p.Name)
	forceLogin := intent != "" && !strings.HasPrefix(intent, intentNext+intentSep)
	authURL, state, err := h.oauth.AuthCodeURLWithIntent(c.Request().Context(), p, redirectURI, intent, forceLogin)
	if err != nil {
		return c.AbortInternalServerError("failed to start oauth flow", err)
	}
	setFlowCookie(c, oauthStateCookie, state, oauth.StateTTL, http.SameSiteLaxMode)
	c.Redirect(http.StatusFound, authURL)
	return nil
}

func (h *OAuthHandler) Callback(c *okapi.Context) error {
	slug := c.Param("slug")
	if e := c.Query("error"); e != "" {
		return h.fail(c, e)
	}
	code := c.Query("code")
	state := c.Query("state")
	if code == "" || state == "" {
		return h.fail(c, "missing_code")
	}
	browserState := cookieValue(c, oauthStateCookie)
	setFlowCookie(c, oauthStateCookie, "", 0, http.SameSiteLaxMode)
	if subtle.ConstantTimeCompare([]byte(browserState), []byte(state)) != 1 {
		return h.fail(c, "invalid_state")
	}
	boundSlug, err := h.oauth.ConsumeState(c.Request().Context(), state)
	if err != nil || boundSlug != slug {
		return h.fail(c, "invalid_state")
	}
	p, err := h.oauth.Get(slug)
	if err != nil {
		return h.fail(c, "provider_unavailable")
	}

	user, err := h.oauth.Authenticate(c.Request().Context(), p, code, h.callbackURI(c, slug))
	if err != nil {
		return h.fail(c, oauthErrorCode(err))
	}

	intent := h.oauth.ConsumeIntent(c.Request().Context(), state)
	// The identity provider's login is one factor; an account with 2FA still owes its own.
	if user.TwoFactorEnabled {
		id, err := h.oauth.BeginTwoFactor(c.Request().Context(), oauth.TwoFactorChallenge{UserID: user.ID, Provider: slug, Intent: intent})
		if err != nil {
			return h.fail(c, "token_error")
		}
		setFlowCookie(c, oauthTwoFactorCookie, id, oauth.StateTTL, http.SameSiteStrictMode)
		c.Redirect(http.StatusFound, strings.TrimRight(h.cfg.AppWebURL, "/")+"/login?sso_two_factor=1")
		return nil
	}

	target, code := h.complete(c, user, slug, intent)
	if code != "" {
		return h.fail(c, code)
	}
	c.Redirect(http.StatusFound, target)
	return nil
}

// OAuthTwoFactorRequest carries the second factor for a parked SSO sign-in.
type OAuthTwoFactorRequest struct {
	Body struct {
		Code string `json:"code" required:"true"`
	} `json:"body"`
}

// OAuthTwoFactorResponse is where the browser goes once the sign-in completes.
type OAuthTwoFactorResponse struct {
	Redirect string `json:"redirect"`
}

func (h *OAuthHandler) CompleteTwoFactor(c *okapi.Context, req *OAuthTwoFactorRequest) error {
	ctx := c.Request().Context()
	id := cookieValue(c, oauthTwoFactorCookie)
	ch, err := h.oauth.PendingTwoFactor(ctx, id)
	if err != nil {
		return c.AbortUnauthorized("sign-in expired, start again")
	}
	user, err := h.oauth.User(ch.UserID)
	if err != nil {
		return c.AbortUnauthorized("sign-in expired, start again")
	}
	if !h.auth.VerifyLoginCode(user, strings.TrimSpace(req.Body.Code)) {
		h.oauth.FailTwoFactor(ctx, id)
		return c.AbortUnauthorized("invalid two-factor code")
	}
	if !h.oauth.EndTwoFactor(ctx, id) {
		return c.AbortUnauthorized("sign-in expired, start again")
	}
	setFlowCookie(c, oauthTwoFactorCookie, "", 0, http.SameSiteStrictMode)

	target, code := h.complete(c, user, ch.Provider, ch.Intent)
	if code != "" {
		return c.AbortUnauthorized("sign-in failed: " + code)
	}
	return ok(c, OAuthTwoFactorResponse{Redirect: target})
}

func (h *OAuthHandler) complete(c *okapi.Context, user *models.User, slug, intent string) (string, string) {
	next := ""
	if rest, isNext := strings.CutPrefix(intent, intentNext+intentSep); isNext {
		next, intent = SafeNext(rest), ""
	}
	if h.loginTokens != nil && intent != "" {
		if kind, redirect, cliState, scopes, ok := parseIntent(intent); ok {
			switch kind {
			case intentLoginToken:
				return h.issueLoginToken(c, user, slug, scopes)
			case intentCliLogin:
				return h.issueCliLoginToken(c, user, slug, redirect, cliState, scopes)
			}
		}
	}

	token, jti, err := h.auth.IssueToken(user)
	if err != nil {
		return "", "token_error"
	}
	ua := c.Header("User-Agent")
	if len(ua) > 512 {
		ua = ua[:512]
	}
	_ = h.sessions.Create(&models.Session{
		UserID: user.ID, JTI: jti, IPAddress: c.RealIP(), UserAgent: ua,
		ExpiresAt: time.Now().Add(auth.TokenTTL),
	})
	h.oauth.TouchLastLogin(user)
	h.audit.Record(audit.Entry{
		ActorID: &user.ID, Action: "user.login.oauth", TargetType: "user",
		IP: c.RealIP(), Metadata: map[string]any{"provider": slug},
	})

	// The token is never exposed in the address bar or browser history.
	setSessionCookie(c, token)
	return h.successURL(next), ""
}

func (h *OAuthHandler) issueLoginToken(c *okapi.Context, user *models.User, slug string, scopes []string) (string, string) {
	tok, err := h.loginTokens.Issue(user.ID, scopes, nil)
	if err != nil {
		return "", "token_error"
	}
	ref, err := h.loginTokens.Stash(c.Request().Context(), tok)
	if err != nil {
		return "", "token_error"
	}
	h.oauth.TouchLastLogin(user)
	h.audit.Record(audit.Entry{
		ActorID: &user.ID, Action: "user.login_token_issued", TargetType: "user",
		IP: c.RealIP(), Metadata: map[string]any{"provider": slug, "via": "oauth"},
	})
	base := strings.TrimRight(h.cfg.AppWebURL, "/")
	return base + "/request-token?handoff=" + url.QueryEscape(ref), ""
}

// redirect was validated as loopback when the flow started.
func (h *OAuthHandler) issueCliLoginToken(c *okapi.Context, user *models.User, slug, redirect, cliState string, scopes []string) (string, string) {
	tok, err := h.loginTokens.Issue(user.ID, scopes, nil)
	if err != nil {
		return "", "token_error"
	}
	ref, err := h.loginTokens.Stash(c.Request().Context(), tok)
	if err != nil {
		return "", "token_error"
	}
	h.oauth.TouchLastLogin(user)
	h.audit.Record(audit.Entry{
		ActorID: &user.ID, Action: "user.login_token_issued", TargetType: "user",
		IP: c.RealIP(), Metadata: map[string]any{"provider": slug, "via": "oauth_cli"},
	})
	return buildCliRedirect(redirect, ref, cliState), ""
}

// The 3-part form is accepted so an intent stored before scopes were carried still completes.
func parseIntent(intent string) (kind, redirect, cliState string, scopes []string, ok bool) {
	parts := strings.Split(intent, intentSep)
	switch len(parts) {
	case 1:
		return parts[0], "", "", nil, true
	case 2:
		return parts[0], "", "", splitScopes(parts[1]), true
	case 3:
		return parts[0], parts[1], parts[2], nil, true
	case 4:
		return parts[0], parts[1], parts[2], splitScopes(parts[3]), true
	default:
		return "", "", "", nil, false
	}
}

// Empty means no narrowing requested, which the service reads as its own default.
func splitScopes(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (h *OAuthHandler) callbackURI(c *okapi.Context, slug string) string {
	base := strings.TrimRight(h.cfg.ApiBaseURL, "/")
	if base == "" {
		base = h.requestOrigin(c) + "/api/v1"
	}
	return base + "/auth/oauth/" + slug + "/callback"
}

// The session rides in the HttpOnly cookie, not the URL.
func (h *OAuthHandler) successURL(next string) string {
	base := strings.TrimRight(h.cfg.AppWebURL, "/")
	if next != "" {
		return base + "/oauth/callback?next=" + url.QueryEscape(next)
	}
	return base + "/oauth/callback"
}

// Refuses anything a browser could read as another origin: a scheme, "//host", or a backslash.
func SafeNext(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > 2048 || raw[0] != '/' || strings.HasPrefix(raw, "//") ||
		strings.ContainsAny(raw, "\\\r\n\t") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return ""
	}
	return raw
}

func (h *OAuthHandler) BackchannelLogout(c *okapi.Context) error {
	c.SetHeader("Cache-Control", "no-store")
	p, err := h.oauth.Get(c.Param("slug"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_request", "error_description": "unknown provider"})
	}
	ctx := c.Request().Context()
	user, err := h.oauth.BackchannelLogout(ctx, p, c.FormValue("logout_token"))
	switch {
	case errors.Is(err, oauth.ErrInvalidLogoutToken), errors.Is(err, oauth.ErrUnsupportedType):
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid_request", "error_description": err.Error()})
	case err != nil:
		return c.AbortInternalServerError("logout failed", err)
	}
	if user != nil {
		if sessions, err := h.sessions.ListByUser(user.ID); err == nil {
			for _, s := range sessions {
				if !s.Revoked && time.Now().Before(s.ExpiresAt) {
					h.auth.Revoke(ctx, s.JTI)
				}
			}
		}
		_ = h.sessions.RevokeAllForUser(user.ID, "")
		h.audit.Record(audit.Entry{ActorID: &user.ID, Action: "user.logout.backchannel", TargetType: "user",
			IP: c.RealIP(), Metadata: okapi.M{"provider": p.Name}})
	}
	c.WriteStatus(http.StatusOK)
	return nil
}

func (h *OAuthHandler) fail(c *okapi.Context, code string) error {
	base := strings.TrimRight(h.cfg.AppWebURL, "/")
	c.Redirect(http.StatusFound, base+"/login?error="+url.QueryEscape(code))
	return nil
}

func (h *OAuthHandler) requestOrigin(c *okapi.Context) string {
	r := c.Request()
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = fwd
	}
	return scheme + "://" + host
}

func oauthErrorCode(err error) string {
	switch {
	case errors.Is(err, oauth.ErrDomainNotAllowed):
		return "domain_not_allowed"
	case errors.Is(err, oauth.ErrRegistrationClosed):
		return "registration_closed"
	case errors.Is(err, oauth.ErrAccountDisabled):
		return "account_disabled"
	case errors.Is(err, oauth.ErrNoEmail):
		return "no_email"
	case errors.Is(err, oauth.ErrEmailNotVerified):
		return "email_not_verified"
	case errors.Is(err, oauth.ErrIdentityConflict):
		return "identity_conflict"
	default:
		return "oauth_failed"
	}
}
