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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/services/oauth"
	"github.com/redis/go-redis/v9"
)

// A valid code and state from another browser is login-CSRF and must be refused.
func TestOAuthCallbackRequiresTheBrowsersState(t *testing.T) {
	mr := miniredis.RunT(t)
	if err := mr.Set("oauth:state:abc", "acme-sso"); err != nil {
		t.Fatal(err)
	}
	h := &OAuthHandler{
		oauth: oauth.NewService(nil, nil, nil, redis.NewClient(&redis.Options{Addr: mr.Addr()})),
		cfg:   &config.Config{AppWebURL: "https://miabi.test"},
	}

	for name, cookie := range map[string]string{"no cookie": "", "another flow's cookie": "xyz"} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/acme-sso/callback?code=c&state=abc", nil)
			if cookie != "" {
				req.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: cookie})
			}
			rec := httptest.NewRecorder()
			c := okapi.NewContext(okapi.New(), rec, req)

			if err := h.Callback(c); err != nil {
				t.Fatal(err)
			}
			if loc := rec.Header().Get("Location"); !strings.HasSuffix(loc, "/login?error=invalid_state") {
				t.Fatalf("redirect = %q, want invalid_state", loc)
			}
			if !mr.Exists("oauth:state:abc") {
				t.Fatal("the state was consumed by a refused callback")
			}
		})
	}
}
