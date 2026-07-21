// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
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
	oauth    *oauth.Service
	repo     *repositories.OAuthProviderRepository
	auth     *auth.Service
	sessions *repositories.SessionRepository
	audit    *audit.Logger
	cfg      *config.Config
	// loginTokens mints a CLI login token when the SSO flow is started with
	// intent=login_token. Set by SetLoginTokens; nil disables that intent.
	loginTokens *logintoken.Service
}

func NewOAuthHandler(oauthSvc *oauth.Service, repo *repositories.OAuthProviderRepository, authSvc *auth.Service, sessions *repositories.SessionRepository, auditLog *audit.Logger, cfg *config.Config) *OAuthHandler {
	return &OAuthHandler{oauth: oauthSvc, repo: repo, auth: authSvc, sessions: sessions, audit: auditLog, cfg: cfg}
}

// SetLoginTokens wires the login-token issuer used by the SSO "Copy login
// command" path (intent=login_token). Optional.
func (h *OAuthHandler) SetLoginTokens(s *logintoken.Service) { h.loginTokens = s }

// intentLoginToken is the SSO intent that mints a CLI token instead of a console
// session on callback.
const intentLoginToken = "login_token"

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

// ListProviders returns enabled, non-hidden providers for login buttons, plus a
// flag indicating whether hidden providers exist (reachable via DiscoverSSO).
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

// DiscoverSSOResponse names the provider to begin the SSO flow with (its handle
// feeds the /{slug}/authorize redirect).
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

// Authorize redirects the browser to the provider's consent screen. With
// ?intent=login_token it forces a fresh IdP login (prompt=login) and the callback
// mints a CLI token instead of a console session — the SSO half of "Copy login
// command".
func (h *OAuthHandler) Authorize(c *okapi.Context) error {
	p, err := h.oauth.Get(c.Param("slug"))
	if err != nil {
		return c.AbortNotFound("provider not found")
	}
	intent := ""
	if c.Query("intent") == intentLoginToken && h.loginTokens != nil {
		intent = intentLoginToken
	}
	redirectURI := h.callbackURI(c, p.Name)
	authURL, err := h.oauth.AuthCodeURLWithIntent(c.Request().Context(), p, redirectURI, intent)
	if err != nil {
		return c.AbortInternalServerError("failed to start oauth flow", err)
	}
	c.Redirect(http.StatusFound, authURL)
	return nil
}

// Callback completes the flow: exchanges the code, provisions/links the user,
// issues a session, and redirects back to the web app with a token.
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

	// CLI login-token intent: the fresh SSO login just proved identity, so mint a
	// short-lived token and hand it off to the display page — no console session.
	if h.oauth.ConsumeIntent(c.Request().Context(), state) == intentLoginToken && h.loginTokens != nil {
		return h.issueLoginToken(c, user, slug)
	}

	token, jti, err := h.auth.IssueToken(user)
	if err != nil {
		return h.fail(c, "token_error")
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

	// Set the session as an HttpOnly cookie and redirect to a clean URL — the
	// token is never exposed in the address bar / browser history.
	setSessionCookie(c, token)
	c.Redirect(http.StatusFound, h.successURL())
	return nil
}

// issueLoginToken mints a CLI login token for a user who just re-authenticated
// via SSO, stashes it under a single-use hand-off reference, and redirects the
// browser to the display page with that reference (never the token itself). This
// is the SSO half of the OpenShift-style "Copy login command": a fresh IdP login
// then a one-time token hand-off, so the secret never rides in the redirect URL.
func (h *OAuthHandler) issueLoginToken(c *okapi.Context, user *models.User, slug string) error {
	tok, err := h.loginTokens.Issue(user.ID, nil, nil)
	if err != nil {
		return h.fail(c, "token_error")
	}
	ref, err := h.loginTokens.Stash(c.Request().Context(), tok)
	if err != nil {
		return h.fail(c, "token_error")
	}
	h.oauth.TouchLastLogin(user)
	h.audit.Record(audit.Entry{
		ActorID: &user.ID, Action: "user.login_token_issued", TargetType: "user",
		IP: c.RealIP(), Metadata: map[string]any{"provider": slug, "via": "oauth"},
	})
	base := strings.TrimRight(h.cfg.AppWebURL, "/")
	c.Redirect(http.StatusFound, base+"/request-token?handoff="+url.QueryEscape(ref))
	return nil
}

// --- helpers ---

// callbackURI returns the absolute redirect URI registered with the provider.
func (h *OAuthHandler) callbackURI(c *okapi.Context, slug string) string {
	base := strings.TrimRight(h.cfg.ApiBaseURL, "/")
	if base == "" {
		base = h.requestOrigin(c) + "/api/v1"
	}
	return base + "/auth/oauth/" + slug + "/callback"
}

// successURL points the browser at the SPA's OAuth callback page. The session is
// carried by the HttpOnly cookie set on the redirect, not in the URL.
func (h *OAuthHandler) successURL() string {
	base := strings.TrimRight(h.cfg.AppWebURL, "/")
	return base + "/oauth/callback"
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
	default:
		return "oauth_failed"
	}
}
