// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package logintoken issues the short-lived personal API token behind the web
// console's "Copy login command" flow (OpenShift-style). It is a thin policy
// layer over the API-key service: a login token is just a personal API key with
// a short expiry, a recognizable name, and a scope set that never includes admin.
//
// It also renders the ready-to-paste CLI and curl commands, and provides a
// single-use Redis hand-off so the SSO (OAuth) path can deliver a freshly minted
// token to the display page without ever putting the secret in a redirect URL.
package logintoken

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/auth"
)

// ErrAdminScope is returned when a caller requests admin/all scope for a login
// token. Login tokens are deliberately capped below admin: a leaked CLI token
// must not be able to do more than the user can undo.
var ErrAdminScope = errors.New("admin scope is not allowed for login tokens")

// ErrInvalidScope wraps an unknown/invalid requested scope so handlers can map it
// to a 400 without importing the auth package's scope validation.
var ErrInvalidScope = errors.New("invalid scope")

// ErrHandoff is returned when a hand-off reference is missing, already claimed,
// or expired.
var ErrHandoff = errors.New("login token is no longer available")

const (
	handoffPrefix = "logintoken:handoff:"
	handoffTTL    = 5 * time.Minute
)

// Service mints and renders login tokens.
type Service struct {
	keys      *auth.APIKeyService
	redis     *redis.Client
	serverURL string        // MIABI_API_URL — the URL the CLI/curl target
	ttl       time.Duration // default lifetime
	maxTTL    time.Duration // hard ceiling a caller may request
}

// New builds the service. serverURL is the public API base URL; ttl/maxTTL come
// from config (hours). A zero maxTTL disables the cap check (still bounded by ttl
// when no override is requested).
func New(keys *auth.APIKeyService, rdb *redis.Client, serverURL string, ttl, maxTTL time.Duration) *Service {
	return &Service{keys: keys, redis: rdb, serverURL: strings.TrimRight(serverURL, "/"), ttl: ttl, maxTTL: maxTTL}
}

// Token is a freshly minted login token plus everything the display page shows.
// The plaintext is present only in this value, only once — it is never stored.
type Token struct {
	Token        string    `json:"token"`
	SHA256       string    `json:"sha256"` // fingerprint of the plaintext (how it is stored/identified)
	ExpiresAt    time.Time `json:"expires_at"`
	Scopes       []string  `json:"scopes"`
	ServerURL    string    `json:"server_url"`
	LoginCommand string    `json:"login_command"`
	CurlExample  string    `json:"curl_example"`
}

// resolveScopes applies the login-token scope policy: default to console-
// equivalent (read/write/deploy) and reject admin or "*".
func resolveScopes(requested []string) ([]string, error) {
	if len(requested) == 0 {
		return []string{models.ScopeRead, models.ScopeWrite, models.ScopeDeploy}, nil
	}
	norm, err := auth.NormalizeScopes(requested)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidScope, err)
	}
	for _, s := range norm {
		if s == models.ScopeAdmin || s == models.ScopeAll {
			return nil, ErrAdminScope
		}
	}
	return norm, nil
}

// resolveExpiry picks the token lifetime: the requested hours clamped to
// (0, maxTTL], or the default ttl when none is requested.
func (s *Service) resolveExpiry(requestedHours *int) time.Time {
	d := s.ttl
	if requestedHours != nil && *requestedHours > 0 {
		d = time.Duration(*requestedHours) * time.Hour
	}
	if s.maxTTL > 0 && d > s.maxTTL {
		d = s.maxTTL
	}
	return time.Now().Add(d)
}

// Issue mints a personal, short-lived API key for userID and renders the CLI and
// curl commands. requestedScopes/requestedHours are optional overrides.
func (s *Service) Issue(userID uint, requestedScopes []string, requestedHours *int) (*Token, error) {
	scopes, err := resolveScopes(requestedScopes)
	if err != nil {
		return nil, err
	}
	exp := s.resolveExpiry(requestedHours)
	name := fmt.Sprintf("CLI login token · %s", time.Now().UTC().Format("2006-01-02 15:04"))

	// Personal key (nil workspace): reaches all of the user's workspaces at request
	// time, like the console, and does not count against any workspace quota.
	plaintext, _, err := s.keys.Create(userID, nil, name, nil, scopes, &exp)
	if err != nil {
		return nil, err
	}

	sum := sha256.Sum256([]byte(plaintext))
	return &Token{
		Token:        plaintext,
		SHA256:       hex.EncodeToString(sum[:]),
		ExpiresAt:    exp,
		Scopes:       scopes,
		ServerURL:    s.serverURL,
		LoginCommand: s.loginCommand(plaintext),
		CurlExample:  s.curlExample(plaintext),
	}, nil
}

func (s *Service) loginCommand(token string) string {
	if s.serverURL == "" {
		return fmt.Sprintf("miabi login --token %s --server <your-miabi-url>", token)
	}
	return fmt.Sprintf("miabi login --token %s --server %s", token, s.serverURL)
}

func (s *Service) curlExample(token string) string {
	base := s.serverURL
	if base == "" {
		base = "<your-miabi-url>"
	}
	return fmt.Sprintf("curl -H \"Authorization: Bearer %s\" %s/api/v1/me", token, base)
}

// Stash stores a minted token under a single-use, short-TTL hand-off reference,
// for the OAuth path to deliver it to the display page out of band (never in a
// redirect URL). Returns the opaque hand-off id.
func (s *Service) Stash(ctx context.Context, tok *Token) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	ref := hex.EncodeToString(b)
	data, err := json.Marshal(tok)
	if err != nil {
		return "", err
	}
	if err := s.redis.Set(ctx, handoffPrefix+ref, data, handoffTTL).Err(); err != nil {
		return "", err
	}
	return ref, nil
}

// Claim atomically reads and deletes a hand-off reference, returning the token.
// Single-use: a second claim of the same reference fails.
func (s *Service) Claim(ctx context.Context, ref string) (*Token, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, ErrHandoff
	}
	data, err := s.redis.GetDel(ctx, handoffPrefix+ref).Result()
	if err != nil {
		return nil, ErrHandoff
	}
	var tok Token
	if err := json.Unmarshal([]byte(data), &tok); err != nil {
		return nil, ErrHandoff
	}
	return &tok, nil
}
