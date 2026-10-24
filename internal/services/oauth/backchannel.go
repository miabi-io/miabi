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

package oauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/miabi-io/miabi/internal/models"
)

// backchannelLogoutEvent is the events member that marks an OpenID Connect Back-Channel Logout token.
const backchannelLogoutEvent = "http://schemas.openid.net/event/backchannel-logout"

const (
	jwksTTL         = time.Hour
	jwksMinRefresh  = time.Minute
	logoutJTIPrefix = "miabi:oauth:logout-jti:"
)

// ErrInvalidLogoutToken is returned for a logout token that does not verify; the endpoint answers 400.
var ErrInvalidLogoutToken = errors.New("invalid logout token")

type jwksEntry struct {
	keys    map[string]any
	fetched time.Time
}

type jwksCache struct {
	mu      sync.Mutex
	entries map[string]*jwksEntry
}

// BackchannelLogout verifies an OpenID Connect Back-Channel Logout token from p and returns the
// linked user it names, nil when nobody is linked to that subject. When the token carries the
// provider's groups claim, those groups are applied as at sign-in, so a role the provider took
// away goes now rather than at the next sign-in. The caller ends the user's sessions.
func (s *Service) BackchannelLogout(ctx context.Context, p *models.OAuthProvider, raw string) (*models.User, error) {
	if p.Type != models.OAuthProviderOIDC || strings.TrimSpace(p.Issuer) == "" {
		return nil, ErrUnsupportedType
	}
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return s.verificationKey(ctx, p, kid)
	}, jwt.WithValidMethods([]string{"ES256", "RS256"}), jwt.WithAudience(p.ClientID), jwt.WithIssuedAt(),
		jwt.WithExpirationRequired(), jwt.WithLeeway(30*time.Second))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidLogoutToken, err)
	}
	if iss, _ := claims["iss"].(string); strings.TrimRight(iss, "/") != strings.TrimRight(p.Issuer, "/") {
		return nil, fmt.Errorf("%w: issuer mismatch", ErrInvalidLogoutToken)
	}
	events, _ := claims["events"].(map[string]any)
	if _, ok := events[backchannelLogoutEvent]; !ok {
		return nil, fmt.Errorf("%w: not a logout token", ErrInvalidLogoutToken)
	}
	// A nonce would let an ID token pass as a logout token (spec §2.6).
	if _, ok := claims["nonce"]; ok {
		return nil, fmt.Errorf("%w: nonce present", ErrInvalidLogoutToken)
	}
	sub, _ := claims["sub"].(string)
	jti, _ := claims["jti"].(string)
	if sub == "" || jti == "" {
		return nil, fmt.Errorf("%w: sub and jti are required", ErrInvalidLogoutToken)
	}
	if s.redis != nil {
		exp, _ := claims.GetExpirationTime()
		ttl := time.Until(exp.Time) + time.Minute
		fresh, err := s.redis.SetNX(ctx, logoutJTIPrefix+p.Name+":"+jti, "1", ttl).Result()
		if err != nil {
			return nil, err
		}
		if !fresh {
			return nil, fmt.Errorf("%w: replayed", ErrInvalidLogoutToken)
		}
	}
	user, err := s.linkedUser(p, sub)
	if err != nil || user == nil {
		return nil, err
	}
	if _, ok := claims[p.GroupsClaim]; ok && s.groupSync != nil && p.GroupsClaim != "" {
		s.groupSync(p, user, listClaim(claims, p.GroupsClaim))
	}
	return user, nil
}

func (s *Service) verificationKey(ctx context.Context, p *models.OAuthProvider, kid string) (any, error) {
	s.jwks.mu.Lock()
	defer s.jwks.mu.Unlock()
	if s.jwks.entries == nil {
		s.jwks.entries = map[string]*jwksEntry{}
	}
	e := s.jwks.entries[p.Issuer]
	if e != nil && time.Since(e.fetched) < jwksTTL {
		if k := pickKey(e.keys, kid); k != nil {
			return k, nil
		}
		if time.Since(e.fetched) < jwksMinRefresh {
			return nil, errors.New("unknown signing key")
		}
	}
	keys, err := s.fetchJWKS(ctx, p.Issuer)
	if err != nil {
		return nil, err
	}
	s.jwks.entries[p.Issuer] = &jwksEntry{keys: keys, fetched: time.Now()}
	if k := pickKey(keys, kid); k != nil {
		return k, nil
	}
	return nil, errors.New("unknown signing key")
}

func pickKey(keys map[string]any, kid string) any {
	if kid != "" {
		return keys[kid]
	}
	if len(keys) == 1 {
		for _, k := range keys {
			return k
		}
	}
	return nil
}

func (s *Service) getJSON(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %d", u, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Use string `json:"use"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func (s *Service) fetchJWKS(ctx context.Context, issuer string) (map[string]any, error) {
	var doc struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := s.getJSON(ctx, strings.TrimRight(issuer, "/")+"/.well-known/openid-configuration", &doc); err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	if doc.JWKSURI == "" {
		return nil, errors.New("oidc discovery returned no jwks_uri")
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := s.getJSON(ctx, doc.JWKSURI, &set); err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}
	out := map[string]any{}
	for _, k := range set.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		if pub, err := k.publicKey(); err == nil {
			out[k.Kid] = pub
		}
	}
	return out, nil
}

func (k jwk) publicKey() (any, error) {
	dec := base64.RawURLEncoding.DecodeString
	switch k.Kty {
	case "EC":
		if k.Crv != "P-256" {
			return nil, errors.New("unsupported curve")
		}
		x, err := dec(k.X)
		if err != nil {
			return nil, err
		}
		y, err := dec(k.Y)
		if err != nil {
			return nil, err
		}
		if len(x) != 32 || len(y) != 32 {
			return nil, errors.New("malformed P-256 coordinates")
		}
		return ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
	case "RSA":
		n, err := dec(k.N)
		if err != nil {
			return nil, err
		}
		e, err := dec(k.E)
		if err != nil {
			return nil, err
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, nil
	}
	return nil, errors.New("unsupported key type")
}
