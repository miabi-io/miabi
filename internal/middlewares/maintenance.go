// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package middlewares

import (
	"fmt"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/settings"
)

// maintenanceAllow lists path fragments that stay reachable during maintenance
// so admins can still authenticate and operate the platform.
var maintenanceAllow = []string{
	"/auth/", "/healthz", "/readyz", "/info", "/metrics", "/admin/", "/system/",
}

// Maintenance is a group-level middleware that returns 503 for non-admins while maintenance mode
// is enabled. It reads the caller's role straight from the JWT so it can run ahead of the
// per-route auth middleware. Admins, unauthenticated requests and allow-listed paths pass through.
func Maintenance(cfg *config.Config, provider *settings.Provider) okapi.Middleware {
	secret := []byte(cfg.JWTSecret)
	return func(c *okapi.Context) error {
		if !provider.Bool(settings.KeyMaintenanceMode, false) {
			return c.Next()
		}
		path := c.Request().URL.Path
		for _, p := range maintenanceAllow {
			if strings.Contains(path, p) {
				return c.Next()
			}
		}
		if roleFromBearer(c, secret) == string(models.SystemRoleAdmin) {
			return c.Next()
		}
		return c.AbortWithStatus(503, "the platform is under maintenance")
	}
}

// roleFromBearer extracts the role claim from the Authorization bearer token,
// returning "" when absent or invalid (treated as non-admin).
func roleFromBearer(c *okapi.Context, secret []byte) string {
	raw := strings.TrimSpace(strings.TrimPrefix(c.Header("Authorization"), "Bearer "))
	if raw == "" {
		raw = strings.TrimSpace(c.Query("token"))
	}
	if raw == "" || strings.HasPrefix(raw, "mb_") {
		return ""
	}
	// Pin the algorithm to HMAC: the platform signs with a symmetric secret, so
	// rejecting any other alg closes the classic alg-confusion attack (a token
	// forged with alg:none or an asymmetric alg must never validate here).
	token, err := jwt.Parse(raw, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil || !token.Valid {
		return ""
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return ""
	}
	role, _ := claims["role"].(string)
	return role
}
