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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
)

// A service account promoted before promotion was refused must not reach the admin API via its role column.
func TestRequireSystemAdminRefusesAServiceAccount(t *testing.T) {
	h := newSessionHarness(t)
	ws := uint(3)
	for _, tc := range []struct {
		name string
		user models.User
		want int
	}{
		{"service account with role admin", models.User{Email: "sa@" + models.ServiceAccountEmailDomain, Username: "sa-x",
			PasswordHash: "!", Role: models.SystemRoleAdmin, Active: true, Kind: models.UserKindService, ServiceWorkspaceID: &ws}, http.StatusForbidden},
		{"human admin", models.User{Email: "admin@example.com", Username: "admin", PasswordHash: "x",
			Role: models.SystemRoleAdmin, Active: true, Kind: models.UserKindHuman}, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := tc.user
			if err := h.users.Create(&u); err != nil {
				t.Fatal(err)
			}
			app := okapi.New()
			seed := func(c *okapi.Context) error {
				c.Set(CtxUserID, int(u.ID))
				c.Set(CtxAuthMethod, "jwt")
				return c.Next()
			}
			app.Get("/admin", func(c *okapi.Context) error { return c.JSON(http.StatusOK, nil) },
				okapi.UseMiddleware(seed), okapi.UseMiddleware(RequireSystemAdmin(h.users, nil)))
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))
			if rec.Code != tc.want {
				t.Errorf("status %d, want %d", rec.Code, tc.want)
			}
		})
	}
}
