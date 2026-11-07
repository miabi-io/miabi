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

var maintenanceAllow = []string{
	"/auth/", "/healthz", "/readyz", "/info", "/metrics", "/admin/", "/system/",
}

// Reads the role straight from the JWT so it can run ahead of the per-route auth middleware.
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

func roleFromBearer(c *okapi.Context, secret []byte) string {
	raw := strings.TrimSpace(strings.TrimPrefix(c.Header("Authorization"), "Bearer "))
	if raw == "" {
		raw = strings.TrimSpace(c.Query("token"))
	}
	if raw == "" || strings.HasPrefix(raw, "mb_") {
		return ""
	}
	// Pin HMAC to close the alg-confusion attack (alg:none or an asymmetric alg).
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
