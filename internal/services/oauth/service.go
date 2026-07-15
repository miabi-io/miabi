// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package oauth implements the OAuth 2.0 / OpenID Connect authorization-code
// login flow against admin-configured providers. It resolves provider endpoints
// (Google well-known or generic OIDC discovery), builds authorize URLs, exchanges
// codes, and provisions/links users.
package oauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrProviderNotFound   = errors.New("oauth provider not found")
	ErrProviderDisabled   = errors.New("oauth provider is disabled")
	ErrInvalidState       = errors.New("invalid or expired state")
	ErrDomainNotAllowed   = errors.New("email domain is not allowed for this provider")
	ErrRegistrationClosed = errors.New("auto-registration is disabled for this provider")
	ErrAccountDisabled    = errors.New("account is disabled")
	ErrNoEmail            = errors.New("provider did not return an email address")
	ErrUnsupportedType    = errors.New("unsupported provider type")
)

const statePrefix = "oauth:state:"
const stateIntentPrefix = "oauth:intent:"
const stateTTL = 10 * time.Minute

// Google OAuth 2.0 / OIDC endpoints.
const (
	googleIssuer      = "https://accounts.google.com"
	googleAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL    = "https://oauth2.googleapis.com/token"
	googleUserInfoURL = "https://openidconnect.googleapis.com/v1/userinfo"
)

// UserInfo is the subset of provider claims Miabi consumes.
type UserInfo struct {
	Sub   string `json:"sub"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type Service struct {
	providers  *repositories.OAuthProviderRepository
	users      *repositories.UserRepository
	workspaces *repositories.WorkspaceRepository
	// membershipGate optionally reports whether a user may join one more workspace
	// (the per-user membership limit). Nil-safe; when it returns an error the
	// best-effort auto-join is skipped rather than failing the sign-in.
	membershipGate func(userID uint) error
	redis          *redis.Client
	http           *http.Client
}

func NewService(providers *repositories.OAuthProviderRepository, users *repositories.UserRepository, redisClient *redis.Client) *Service {
	return &Service{
		providers: providers,
		users:     users,
		redis:     redisClient,
		http:      &http.Client{Timeout: 15 * time.Second},
	}
}

// TouchLastLogin stamps and persists the user's last-login time after a
// successful OAuth sign-in.
func (s *Service) TouchLastLogin(user *models.User) {
	now := time.Now()
	user.LastLoginAt = &now
	_ = s.users.Update(user)
}

// SetWorkspaces wires the workspace repository so a newly registered SSO user can
// be auto-joined to a provider's configured default workspace. Optional: without
// it, auto-join is skipped (the rest of the flow is unaffected).
func (s *Service) SetWorkspaces(workspaces *repositories.WorkspaceRepository) {
	s.workspaces = workspaces
}

// SetMembershipGate wires the per-user membership-limit check so SSO auto-join
// respects it (over-limit users are simply not auto-joined). Optional/nil-safe.
func (s *Service) SetMembershipGate(gate func(userID uint) error) {
	s.membershipGate = gate
}

// ResolveEndpoints fills in AuthURL/TokenURL/UserInfoURL/Issuer for a provider
// based on its type. Called by the admin handler before persisting. For OIDC it
// performs discovery against the issuer's well-known configuration when explicit
// endpoints are not supplied.
func (s *Service) ResolveEndpoints(ctx context.Context, p *models.OAuthProvider) error {
	switch p.Type {
	case models.OAuthProviderGoogle:
		p.Issuer = googleIssuer
		p.AuthURL = googleAuthURL
		p.TokenURL = googleTokenURL
		p.UserInfoURL = googleUserInfoURL
		if strings.TrimSpace(p.Scopes) == "" {
			p.Scopes = "openid email profile"
		}
		return nil
	case models.OAuthProviderOIDC:
		if p.AuthURL != "" && p.TokenURL != "" && p.UserInfoURL != "" {
			return nil
		}
		if strings.TrimSpace(p.Issuer) == "" {
			return errors.New("oidc provider requires an issuer or explicit endpoints")
		}
		return s.discover(ctx, p)
	default:
		return ErrUnsupportedType
	}
}

func (s *Service) discover(ctx context.Context, p *models.OAuthProvider) error {
	wellKnown := strings.TrimRight(p.Issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wellKnown, nil)
	if err != nil {
		return err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("oidc discovery failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("oidc discovery returned %d", resp.StatusCode)
	}
	var doc struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		UserInfoEndpoint      string `json:"userinfo_endpoint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return fmt.Errorf("oidc discovery decode: %w", err)
	}
	if p.AuthURL == "" {
		p.AuthURL = doc.AuthorizationEndpoint
	}
	if p.TokenURL == "" {
		p.TokenURL = doc.TokenEndpoint
	}
	if p.UserInfoURL == "" {
		p.UserInfoURL = doc.UserInfoEndpoint
	}
	if p.AuthURL == "" || p.TokenURL == "" || p.UserInfoURL == "" {
		return errors.New("oidc discovery did not return all required endpoints")
	}
	return nil
}

// Get returns an enabled provider by name (handle).
func (s *Service) Get(name string) (*models.OAuthProvider, error) {
	p, err := s.providers.FindByName(name)
	if err != nil {
		return nil, ErrProviderNotFound
	}
	if !p.Enabled {
		return nil, ErrProviderDisabled
	}
	return p, nil
}

// AuthCodeURL stores a fresh state token (in Redis) and returns the provider's
// authorization URL for the redirect.
func (s *Service) AuthCodeURL(ctx context.Context, p *models.OAuthProvider, redirectURI string) (string, error) {
	return s.AuthCodeURLWithIntent(ctx, p, redirectURI, "")
}

// AuthCodeURLWithIntent is AuthCodeURL with an application-level intent bound to
// the state (e.g. "login_token"), read back in the callback via ConsumeIntent.
// When intent is non-empty it also sets prompt=login so the IdP forces a fresh
// authentication — the re-auth property the CLI-token flow depends on. This
// mirrors OpenShift's request-token flow, which likewise forces a fresh login
// before minting a token, so an ambient SSO session can't silently issue one.
func (s *Service) AuthCodeURLWithIntent(ctx context.Context, p *models.OAuthProvider, redirectURI, intent string) (string, error) {
	state, err := randomState()
	if err != nil {
		return "", err
	}
	if err := s.redis.Set(ctx, statePrefix+state, p.Name, stateTTL).Err(); err != nil {
		return "", err
	}
	if intent != "" {
		if err := s.redis.Set(ctx, stateIntentPrefix+state, intent, stateTTL).Err(); err != nil {
			return "", err
		}
	}
	q := url.Values{}
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", scopesOrDefault(p.Scopes))
	q.Set("state", state)
	if intent != "" {
		q.Set("prompt", "login") // force fresh IdP authentication
	}
	sep := "?"
	if strings.Contains(p.AuthURL, "?") {
		sep = "&"
	}
	return p.AuthURL + sep + q.Encode(), nil
}

// ConsumeIntent reads and deletes the intent bound to a state (empty when none).
// Safe to call after ConsumeState — the intent is stored under a separate key.
func (s *Service) ConsumeIntent(ctx context.Context, state string) string {
	if strings.TrimSpace(state) == "" {
		return ""
	}
	intent, err := s.redis.GetDel(ctx, stateIntentPrefix+state).Result()
	if err != nil {
		return ""
	}
	return intent
}

// ConsumeState validates and deletes a state token, returning the bound slug.
func (s *Service) ConsumeState(ctx context.Context, state string) (string, error) {
	if strings.TrimSpace(state) == "" {
		return "", ErrInvalidState
	}
	key := statePrefix + state
	slug, err := s.redis.Get(ctx, key).Result()
	if err != nil {
		return "", ErrInvalidState
	}
	s.redis.Del(ctx, key)
	return slug, nil
}

// Authenticate exchanges an authorization code for user info and returns the
// matching local user, provisioning a new one when AutoRegister permits.
func (s *Service) Authenticate(ctx context.Context, p *models.OAuthProvider, code, redirectURI string) (*models.User, error) {
	info, err := s.exchange(ctx, p, code, redirectURI)
	if err != nil {
		return nil, err
	}
	email := strings.ToLower(strings.TrimSpace(info.Email))
	if email == "" {
		return nil, ErrNoEmail
	}
	if !domainAllowed(email, p.AllowedDomains) {
		return nil, ErrDomainNotAllowed
	}

	user, err := s.users.FindByEmail(email)
	if err == nil {
		if !user.Active {
			return nil, ErrAccountDisabled
		}
		return user, nil
	}

	if !p.AutoRegister {
		return nil, ErrRegistrationClosed
	}
	name := strings.TrimSpace(info.Name)
	if name == "" {
		name = email
	}
	// First registered user becomes the platform admin (mirrors password flow).
	count, _ := s.users.Count()
	role := models.SystemRoleUser
	if count == 0 {
		role = models.SystemRoleAdmin
	}
	now := time.Now()
	newUser := &models.User{
		Name:            name,
		Email:           email,
		PasswordHash:    unusablePassword(),
		Role:            role,
		Active:          true,
		EmailVerifiedAt: &now, // provider asserts the email
	}
	if err := s.users.Create(newUser); err != nil {
		return nil, err
	}
	s.autoJoinWorkspace(p, newUser.ID)
	return newUser, nil
}

// autoJoinWorkspace adds a freshly registered SSO user to the provider's
// configured default workspace with the configured role. Best-effort: a missing
// workspace repo, no configured workspace, or an add failure never blocks login.
func (s *Service) autoJoinWorkspace(p *models.OAuthProvider, userID uint) {
	if s.workspaces == nil || p.DefaultWorkspaceID == nil {
		return
	}
	role := p.DefaultRole
	if !role.Valid() {
		role = models.WorkspaceRoleViewer
	}
	// Respect the per-user membership limit (a non-owner auto-join). Over-limit
	// users just aren't auto-joined — sign-in still succeeds.
	if s.membershipGate != nil && role != models.WorkspaceRoleOwner {
		if err := s.membershipGate(userID); err != nil {
			logger.Info("oauth: auto-join skipped (membership limit)", "workspace", *p.DefaultWorkspaceID, "user", userID)
			return
		}
	}
	if err := s.workspaces.AddMember(&models.WorkspaceMember{
		WorkspaceID: *p.DefaultWorkspaceID, UserID: userID, Role: role,
	}); err != nil {
		logger.Warn("oauth: auto-join workspace failed", "workspace", *p.DefaultWorkspaceID, "user", userID, "error", err)
	}
}

// ProvisionSSOUser finds a user by email or creates one from a trusted identity
// provider (e.g. SAML). Mirrors the OAuth auto-register policy: the first user is
// the platform admin, the asserted email is treated as verified, and the account
// uses an unusable password. Returns ErrAccountDisabled for a disabled user.
func (s *Service) ProvisionSSOUser(ctx context.Context, email, name string) (*models.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, ErrNoEmail
	}
	if user, err := s.users.FindByEmail(email); err == nil {
		if !user.Active {
			return nil, ErrAccountDisabled
		}
		return user, nil
	}
	if strings.TrimSpace(name) == "" {
		name = email
	}
	count, _ := s.users.Count()
	role := models.SystemRoleUser
	if count == 0 {
		role = models.SystemRoleAdmin
	}
	now := time.Now()
	user := &models.User{
		Name: name, Email: email, PasswordHash: unusablePassword(),
		Role: role, Active: true, EmailVerifiedAt: &now,
	}
	if err := s.users.Create(user); err != nil {
		return nil, err
	}
	return user, nil
}

func (s *Service) exchange(ctx context.Context, p *models.OAuthProvider, code, redirectURI string) (*UserInfo, error) {
	secret, err := crypto.Decrypt(p.ClientSecretEnc)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt client secret: %w", err)
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", p.ClientID)
	form.Set("client_secret", secret)
	form.Set("redirect_uri", redirectURI)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return nil, fmt.Errorf("token decode: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, errors.New("token endpoint returned no access token")
	}

	infoReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.UserInfoURL, nil)
	if err != nil {
		return nil, err
	}
	infoReq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	infoReq.Header.Set("Accept", "application/json")
	infoResp, err := s.http.Do(infoReq)
	if err != nil {
		return nil, fmt.Errorf("userinfo request failed: %w", err)
	}
	defer func() { _ = infoResp.Body.Close() }()
	if infoResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo endpoint returned %d", infoResp.StatusCode)
	}
	// Decode into a generic claim map so a provider can map non-standard OIDC
	// claim names (EmailClaim/NameClaim) onto the user fields; standard claims
	// remain the fallback.
	var claims map[string]any
	if err := json.NewDecoder(infoResp.Body).Decode(&claims); err != nil {
		return nil, fmt.Errorf("userinfo decode: %w", err)
	}
	info := UserInfo{
		Sub:   stringClaim(claims, "sub"),
		Email: firstClaim(claims, p.EmailClaim, "email"),
		Name:  firstClaim(claims, p.NameClaim, "name"),
	}
	return &info, nil
}

// firstClaim returns the first non-empty string value among the named claims.
func firstClaim(claims map[string]any, names ...string) string {
	for _, n := range names {
		if v := stringClaim(claims, n); v != "" {
			return v
		}
	}
	return ""
}

// stringClaim reads a claim as a trimmed string (string values only).
func stringClaim(claims map[string]any, name string) string {
	if name == "" {
		return ""
	}
	if v, ok := claims[name].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func randomState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// unusablePassword stores a random bcrypt hash so SSO-only accounts can never be
// signed in with a password.
func unusablePassword() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	hash, _ := bcrypt.GenerateFromPassword(b, bcrypt.DefaultCost)
	return string(hash)
}

func scopesOrDefault(scopes string) string {
	if strings.TrimSpace(scopes) == "" {
		return "openid email profile"
	}
	return scopes
}

// domainAllowed reports whether an email's domain is permitted. An empty
// allow-list permits any domain.
func domainAllowed(email, allowed string) bool {
	allowed = strings.TrimSpace(allowed)
	if allowed == "" {
		return true
	}
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return false
	}
	domain := strings.ToLower(email[at+1:])
	for _, d := range strings.Split(allowed, ",") {
		if strings.ToLower(strings.TrimSpace(d)) == domain {
			return true
		}
	}
	return false
}
