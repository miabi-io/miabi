// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"net/http"
	"strings"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/auth"
)

// setSessionCookie stores the JWT in an HttpOnly, SameSite=Strict cookie so a browser never
// exposes it to JavaScript. CLI and API clients keep using the token from the response body.
// Secure is set whenever the request is HTTPS, so the cookie still works over plain HTTP in dev.
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

func requestIsHTTPS(c *okapi.Context) bool {
	if c.Request().TLS != nil {
		return true
	}
	return strings.EqualFold(c.Header("X-Forwarded-Proto"), "https")
}
