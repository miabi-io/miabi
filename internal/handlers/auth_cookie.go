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
	"strings"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/auth"
)

// HttpOnly so JavaScript never sees the JWT; Secure only on HTTPS so plain-HTTP dev still works.
func setSessionCookie(c *okapi.Context, token string) {
	http.SetCookie(c.ResponseWriter(), &http.Cookie{
		Name:     middlewares.SessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(auth.TokenTTL.Seconds()),
		HttpOnly: true,
		Secure:   requestIsHTTPS(c),
		SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookie(c *okapi.Context) {
	http.SetCookie(c.ResponseWriter(), &http.Cookie{
		Name:     middlewares.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   requestIsHTTPS(c),
		SameSite: http.SameSiteStrictMode,
	})
}

// Binds the SSO request to the browser that started it, refusing login-CSRF callbacks.
const oauthStateCookie = "miabi_oauth_state"

// Keeps the parked sign-in id out of URLs.
const oauthTwoFactorCookie = "miabi_oauth_2fa"

// The state cookie must be Lax: it is read on the IdP's top-level redirect, which Strict would withhold.
func setFlowCookie(c *okapi.Context, name, value string, ttl time.Duration, sameSite http.SameSite) {
	maxAge := int(ttl.Seconds())
	if value == "" {
		maxAge = -1
	}
	http.SetCookie(c.ResponseWriter(), &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   requestIsHTTPS(c),
		SameSite: sameSite,
	})
}

func cookieValue(c *okapi.Context, name string) string {
	ck, err := c.Request().Cookie(name)
	if err != nil {
		return ""
	}
	return ck.Value
}

func requestIsHTTPS(c *okapi.Context) bool {
	if c.Request().TLS != nil {
		return true
	}
	return strings.EqualFold(c.Header("X-Forwarded-Proto"), "https")
}
