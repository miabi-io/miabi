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

// Package logintoken mints short-lived, never-admin personal API keys for "Copy login command", with a single-use SSO hand-off.
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

// Capped below admin so a leaked CLI token can't do more than the user can undo.
var ErrAdminScope = errors.New("admin scope is not allowed for login tokens")

// Lets handlers map it to 400 without importing the auth package's scope validation.
var ErrInvalidScope = errors.New("invalid scope")

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

// A zero maxTTL disables the cap check.
func New(keys *auth.APIKeyService, rdb *redis.Client, serverURL string, ttl, maxTTL time.Duration) *Service {
	return &Service{keys: keys, redis: rdb, serverURL: strings.TrimRight(serverURL, "/"), ttl: ttl, maxTTL: maxTTL}
}

// The plaintext exists only in this value, once; it is never stored.
type Token struct {
	Token        string    `json:"token"`
	SHA256       string    `json:"sha256"` // fingerprint of the plaintext (how it is stored/identified)
	ExpiresAt    time.Time `json:"expires_at"`
	Scopes       []string  `json:"scopes"`
	ServerURL    string    `json:"server_url"`
	LoginCommand string    `json:"login_command"`
	CurlExample  string    `json:"curl_example"`
}

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

func (s *Service) Issue(userID uint, requestedScopes []string, requestedHours *int) (*Token, error) {
	scopes, err := resolveScopes(requestedScopes)
	if err != nil {
		return nil, err
	}
	exp := s.resolveExpiry(requestedHours)
	name := fmt.Sprintf("CLI login token · %s", time.Now().UTC().Format("2006-01-02 15:04"))

	// A personal key reaches all the user's workspaces like the console and counts against no workspace quota.
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

// Stash delivers the token out of band via a single-use reference, never in a redirect URL.
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

// Claim is single-use: it atomically reads and deletes the reference.
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
