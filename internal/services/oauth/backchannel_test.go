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
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/redis/go-redis/v9"
)

func logoutIssuer(t *testing.T) (*httptest.Server, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": srv.URL, "jwks_uri": srv.URL + "/jwks"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		b64 := base64.RawURLEncoding.EncodeToString
		x, y := make([]byte, 32), make([]byte, 32)
		key.X.FillBytes(x)
		key.Y.FillBytes(y)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{
			{"kty": "EC", "crv": "P-256", "kid": "k1", "use": "sig", "x": b64(x), "y": b64(y)},
		}})
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, key
}

func logoutToken(t *testing.T, key *ecdsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestBackchannelLogout(t *testing.T) {
	srv, key := logoutIssuer(t)
	s, db, _ := registerService(t, "staff@acme.test")
	s.http = srv.Client()
	s.redis = redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	user := seedUser(t, db, "staff@acme.test")
	p := &models.OAuthProvider{ID: 1, Name: "miabi-console", Type: models.OAuthProviderOIDC, ClientID: "client-1",
		Issuer: srv.URL + "/", Enabled: true, GroupsClaim: "groups"}
	if err := db.Create(&models.UserIdentity{ProviderID: p.ID, Subject: "console-user", UserID: user.ID}).Error; err != nil {
		t.Fatal(err)
	}
	var synced []string
	syncs := 0
	s.SetGroupSync(func(_ *models.OAuthProvider, _ *models.User, groups []string) { syncs++; synced = groups })

	now := time.Now()
	base := func() jwt.MapClaims {
		return jwt.MapClaims{"iss": srv.URL, "aud": "client-1", "sub": "console-user", "iat": now.Unix(),
			"exp": now.Add(2 * time.Minute).Unix(), "jti": "j",
			"events": map[string]any{backchannelLogoutEvent: map[string]any{}}, "groups": []string{"miabi:acme:owner"}}
	}
	for _, tc := range []struct {
		name   string
		edit   func(jwt.MapClaims)
		user   bool
		groups []string
		err    bool
	}{
		{name: "applies groups and names the user", user: true, groups: []string{"miabi:acme:owner"}},
		{name: "empty groups still sync", edit: func(c jwt.MapClaims) { c["groups"] = []string{} }, user: true, groups: []string{}},
		{name: "no groups claim leaves memberships alone", edit: func(c jwt.MapClaims) { delete(c, "groups") }, user: true},
		{name: "unlinked subject is a no-op", edit: func(c jwt.MapClaims) { c["sub"] = "stranger" }},
		{name: "other audience", edit: func(c jwt.MapClaims) { c["aud"] = "client-2" }, err: true},
		{name: "other issuer", edit: func(c jwt.MapClaims) { c["iss"] = "https://evil.test" }, err: true},
		{name: "an ID token is not a logout token", edit: func(c jwt.MapClaims) { delete(c, "events") }, err: true},
		{name: "nonce", edit: func(c jwt.MapClaims) { c["nonce"] = "n" }, err: true},
		{name: "expired", edit: func(c jwt.MapClaims) { c["exp"] = now.Add(-time.Hour).Unix() }, err: true},
		{name: "no jti", edit: func(c jwt.MapClaims) { delete(c, "jti") }, err: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			syncs, synced = 0, nil
			c := base()
			c["jti"] = tc.name
			if tc.edit != nil {
				tc.edit(c)
			}
			got, err := s.BackchannelLogout(context.Background(), p, logoutToken(t, key, c))
			if tc.err {
				if !errors.Is(err, ErrInvalidLogoutToken) {
					t.Fatalf("err = %v, want ErrInvalidLogoutToken", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (got != nil) != tc.user {
				t.Fatalf("user = %v, want one: %v", got, tc.user)
			}
			wantSyncs := 0
			if tc.groups != nil {
				wantSyncs = 1
			}
			if syncs != wantSyncs || (tc.groups != nil && len(tc.groups)+len(synced) > 0 && !reflect.DeepEqual(synced, tc.groups)) {
				t.Fatalf("synced %d times with %v, want %d with %v", syncs, synced, wantSyncs, tc.groups)
			}
		})
	}

	t.Run("replay is refused", func(t *testing.T) {
		c := base()
		c["jti"] = "once"
		tok := logoutToken(t, key, c)
		if _, err := s.BackchannelLogout(context.Background(), p, tok); err != nil {
			t.Fatal(err)
		}
		if _, err := s.BackchannelLogout(context.Background(), p, tok); !errors.Is(err, ErrInvalidLogoutToken) {
			t.Fatalf("replay err = %v", err)
		}
	})

	t.Run("signed by another key", func(t *testing.T) {
		other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		c := base()
		c["jti"] = "forged"
		if _, err := s.BackchannelLogout(context.Background(), p, logoutToken(t, other, c)); !errors.Is(err, ErrInvalidLogoutToken) {
			t.Fatalf("err = %v", err)
		}
	})
}
