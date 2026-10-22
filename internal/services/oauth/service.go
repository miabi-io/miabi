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

// Package oauth implements the OAuth 2.0 / OpenID Connect authorization-code login flow against
// admin-configured providers. It resolves provider endpoints (Google well-known or generic OIDC discovery),
// builds authorize URLs, exchanges codes, and provisions or links users.
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
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
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
	ErrNoSubject          = errors.New("provider did not return a subject")
	ErrEmailNotVerified   = errors.New("provider did not verify the email address")
	ErrIdentityConflict   = errors.New("account is already linked to another identity at this provider")
	ErrTwoFactorExpired   = errors.New("two-factor sign-in expired")
)

const statePrefix = "oauth:state:"
const stateIntentPrefix = "oauth:intent:"

// StateTTL is how long an authorization request stays valid.
const StateTTL = 10 * time.Minute

const twoFactorPrefix = "oauth:2fa:"
const twoFactorAttemptsPrefix = "oauth:2fa:attempts:"
const twoFactorTTL = 5 * time.Minute

// twoFactorMaxAttempts bounds code guesses per SSO sign-in; the challenge is dropped after the last.
const twoFactorMaxAttempts = 5

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
	// Username is the handle the provider knows the user by. Empty when the
	// provider sends none, in which case Miabi derives one from the email.
	Username string `json:"preferred_username"`
	// EmailVerified is the provider's email_verified claim; false when it sends none.
	EmailVerified bool `json:"email_verified"`
	// Groups is the provider's GroupsClaim, when it names one.
	Groups []string `json:"groups,omitempty"`
}

type Service struct {
	providers  *repositories.OAuthProviderRepository
	users      *repositories.UserRepository
	identities *repositories.UserIdentityRepository
	workspaces *repositories.WorkspaceRepository
	// membershipGate optionally reports whether a user may join one more workspace
	// (the per-user membership limit). Nil-safe; when it returns an error the
	// best-effort auto-join is skipped rather than failing the sign-in.
	membershipGate func(userID uint) error
	// groupSync applies a provider's group claim after sign-in (Enterprise); nil skips it.
	groupSync func(p *models.OAuthProvider, user *models.User, groups []string)
	redis     *redis.Client
	http      *http.Client
}

func NewService(providers *repositories.OAuthProviderRepository, users *repositories.UserRepository, identities *repositories.UserIdentityRepository, redisClient *redis.Client) *Service {
	return &Service{
		providers:  providers,
		users:      users,
		identities: identities,
		redis:      redisClient,
		http:       &http.Client{Timeout: 15 * time.Second},
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
// SetGroupSync wires the group-claim sync run after every successful sign-in through a provider that
// names a groups claim.
func (s *Service) SetGroupSync(f func(p *models.OAuthProvider, user *models.User, groups []string)) {
	s.groupSync = f
}

func (s *Service) syncGroups(p *models.OAuthProvider, user *models.User, info *UserInfo) {
	if s.groupSync != nil && p.GroupsClaim != "" {
		s.groupSync(p, user, info.Groups)
	}
}

func (s *Service) SetMembershipGate(gate func(userID uint) error) {
	s.membershipGate = gate
}

// ResolveEndpoints fills in AuthURL, TokenURL, UserInfoURL and Issuer for a provider based on its type,
// called by the admin handler before persisting. For OIDC it performs discovery against the issuer's
// well-known configuration when explicit endpoints are not supplied.
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
// authorization URL for the redirect, with the state the caller binds to the browser.
func (s *Service) AuthCodeURL(ctx context.Context, p *models.OAuthProvider, redirectURI string) (string, string, error) {
	return s.AuthCodeURLWithIntent(ctx, p, redirectURI, "")
}

// AuthCodeURLWithIntent is AuthCodeURL with an application-level intent bound to the state, read back in the
// callback via ConsumeIntent. A non-empty intent also sets prompt=login so the IdP forces a fresh
// authentication — the re-auth property the CLI-token flow depends on, mirroring OpenShift's request-token.
func (s *Service) AuthCodeURLWithIntent(ctx context.Context, p *models.OAuthProvider, redirectURI, intent string) (string, string, error) {
	state, err := randomState()
	if err != nil {
		return "", "", err
	}
	if err := s.redis.Set(ctx, statePrefix+state, p.Name, StateTTL).Err(); err != nil {
		return "", "", err
	}
	if intent != "" {
		if err := s.redis.Set(ctx, stateIntentPrefix+state, intent, StateTTL).Err(); err != nil {
			return "", "", err
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
	return p.AuthURL + sep + q.Encode(), state, nil
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

// Authenticate exchanges an authorization code for user info and returns the matching local user,
// provisioning a new one when AutoRegister permits.
//
// A returning user is found by the provider's subject. An existing account is linked by email only
// when the provider asserts that email is verified, since many IdPs let users set any address, or
// when an admin set AllowUnverifiedEmail for a directory that never sends the claim.
func (s *Service) Authenticate(ctx context.Context, p *models.OAuthProvider, code, redirectURI string) (*models.User, error) {
	info, err := s.exchange(ctx, p, code, redirectURI)
	if err != nil {
		return nil, err
	}
	if info.Sub == "" {
		return nil, ErrNoSubject
	}
	email := strings.ToLower(strings.TrimSpace(info.Email))
	if email == "" {
		return nil, ErrNoEmail
	}
	if !domainAllowed(email, p.AllowedDomains) {
		return nil, ErrDomainNotAllowed
	}

	user, err := s.linkedUser(p, info.Sub)
	if err != nil {
		return nil, err
	}
	if user == nil {
		if existing, err := s.users.FindByEmail(email); err == nil {
			if !info.EmailVerified && !p.AllowUnverifiedEmail {
				return nil, ErrEmailNotVerified
			}
			if err := s.link(p, info.Sub, existing.ID); err != nil {
				return nil, err
			}
			user = existing
		}
	}
	if user != nil {
		if !user.Active || user.IsService() {
			return nil, ErrAccountDisabled
		}
		s.syncGroups(p, user, info)
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
	var verifiedAt *time.Time
	if info.EmailVerified {
		now := time.Now()
		verifiedAt = &now
	}
	newUser := &models.User{
		Name:            name,
		Email:           email,
		Username:        s.availableUsername(info.Username),
		PasswordHash:    unusablePassword(),
		Role:            role,
		Active:          true,
		EmailVerifiedAt: verifiedAt,
		AuthSource:      models.AuthSourceOAuth,
		// The realm the provider registers into; nil leaves the account in the default
		// organization. Only set here, on an account being created: signing in through a provider
		// never moves somebody who already exists between tenants.
		OrganizationID: p.OrganizationID,
	}
	if err := s.users.Create(newUser); err != nil {
		return nil, err
	}
	if err := s.link(p, info.Sub, newUser.ID); err != nil {
		return nil, err
	}
	s.autoJoinWorkspace(p, newUser.ID)
	s.syncGroups(p, newUser, info)
	return newUser, nil
}

// linkedUser returns the account a provider subject is bound to, or nil when it is bound to none.
// A binding left behind by a deleted account is dropped.
func (s *Service) linkedUser(p *models.OAuthProvider, subject string) (*models.User, error) {
	identity, err := s.identities.FindBySubject(p.ID, subject)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	user, err := s.users.FindByID(identity.UserID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		_ = s.identities.Delete(identity.ID)
		return nil, nil
	}
	return user, err
}

// link binds a provider subject to an account. An account holds one identity per provider, so a
// second IdP account claiming the same email cannot take it over.
func (s *Service) link(p *models.OAuthProvider, subject string, userID uint) error {
	if _, err := s.identities.FindByUser(p.ID, userID); err == nil {
		return ErrIdentityConflict
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return s.identities.Create(&models.UserIdentity{ProviderID: p.ID, Subject: subject, UserID: userID})
}

// TwoFactorChallenge is an SSO sign-in waiting for the account's second factor.
type TwoFactorChallenge struct {
	UserID   uint   `json:"user_id"`
	Provider string `json:"provider"`
	// Intent is the CLI login intent the sign-in was started with, empty for a console session.
	Intent string `json:"intent,omitempty"`
}

// BeginTwoFactor parks an SSO sign-in until the user's second factor is verified and returns the
// challenge id.
func (s *Service) BeginTwoFactor(ctx context.Context, ch TwoFactorChallenge) (string, error) {
	id, err := randomState()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(ch)
	if err != nil {
		return "", err
	}
	if err := s.redis.Set(ctx, twoFactorPrefix+id, raw, twoFactorTTL).Err(); err != nil {
		return "", err
	}
	return id, nil
}

// PendingTwoFactor returns a parked sign-in without consuming it.
func (s *Service) PendingTwoFactor(ctx context.Context, id string) (*TwoFactorChallenge, error) {
	if strings.TrimSpace(id) == "" {
		return nil, ErrTwoFactorExpired
	}
	raw, err := s.redis.Get(ctx, twoFactorPrefix+id).Bytes()
	if err != nil {
		return nil, ErrTwoFactorExpired
	}
	var ch TwoFactorChallenge
	if err := json.Unmarshal(raw, &ch); err != nil {
		return nil, ErrTwoFactorExpired
	}
	return &ch, nil
}

// FailTwoFactor counts a wrong code and drops the challenge once the attempts run out.
func (s *Service) FailTwoFactor(ctx context.Context, id string) {
	key := twoFactorAttemptsPrefix + id
	n, err := s.redis.Incr(ctx, key).Result()
	if err != nil {
		return
	}
	s.redis.Expire(ctx, key, twoFactorTTL)
	if n >= twoFactorMaxAttempts {
		s.redis.Del(ctx, twoFactorPrefix+id, key)
	}
}

// EndTwoFactor consumes a challenge, reporting false when it was already used or expired so two
// concurrent submissions cannot both complete the sign-in.
func (s *Service) EndTwoFactor(ctx context.Context, id string) bool {
	n, err := s.redis.Del(ctx, twoFactorPrefix+id).Result()
	s.redis.Del(ctx, twoFactorAttemptsPrefix+id)
	return err == nil && n == 1
}

// User returns an active, human account by id for completing a parked sign-in.
func (s *Service) User(id uint) (*models.User, error) {
	user, err := s.users.FindByID(id)
	if err != nil {
		return nil, err
	}
	if !user.Active || user.IsService() {
		return nil, ErrAccountDisabled
	}
	return user, nil
}

// availableUsername turns the handle a provider asserted into one that can
// actually be stored, or "" to let User.BeforeCreate derive one from the email.
func (s *Service) availableUsername(claimed string) string {
	return slug.Available(claimed, s.users.ExistsByUsername)
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

// ProvisionSSOUser finds a user by email or creates one from a SAML assertion. Mirrors the OAuth
// auto-register policy: the first user is the platform admin, the asserted email is treated as verified, and
// the account uses an unusable password. Returns ErrAccountDisabled for a disabled user.
//
// An account that already exists keeps its auth source: an email match is account linking, and a
// local account that also signs in through SSO is still the user's own to edit.
func (s *Service) ProvisionSSOUser(ctx context.Context, email, name, username string) (*models.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, ErrNoEmail
	}
	if user, err := s.users.FindByEmail(email); err == nil {
		if !user.Active || user.IsService() {
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
		Name: name, Email: email, Username: s.availableUsername(username),
		PasswordHash: unusablePassword(),
		Role:         role, Active: true, EmailVerifiedAt: &now,
		AuthSource: models.AuthSourceSAML,
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
		Sub:      stringClaim(claims, "sub"),
		Email:    firstClaim(claims, p.EmailClaim, "email"),
		Name:     firstClaim(claims, p.NameClaim, "name"),
		Username: firstClaim(claims, p.UsernameClaim, "preferred_username"),
		// Some providers send the claim as a string.
		EmailVerified: claims["email_verified"] == true || stringClaim(claims, "email_verified") == "true",
		Groups:        listClaim(claims, p.GroupsClaim),
	}
	return &info, nil
}

// listClaim reads a claim holding a list of strings, or one comma-separated string.
func listClaim(claims map[string]any, name string) []string {
	if name == "" {
		return nil
	}
	var out []string
	switch v := claims[name].(type) {
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case string:
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

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
