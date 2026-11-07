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
	"strconv"
	"strings"
	"testing"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// A service account made admin would give its API keys platform-wide power.
func TestAdminActionsRefuseServiceAccounts(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.User{}); err != nil {
		t.Fatal(err)
	}
	users := repositories.NewUserRepository(db)
	ws := uint(7)
	sa := &models.User{
		Name: "deployer", Username: "sa-deployer-abc123", Email: "sa-deployer-abc123@" + models.ServiceAccountEmailDomain,
		PasswordHash: "!", Role: models.SystemRoleUser, Active: true, Kind: models.UserKindService, ServiceWorkspaceID: &ws,
	}
	if err := users.Create(sa); err != nil {
		t.Fatal(err)
	}

	h := NewAdminUserHandler(db, users, nil, nil, nil, nil, nil, nil, 0)
	app := okapi.New()
	app.Patch("/admin/users/{id}", okapi.H(h.Update))
	app.Post("/admin/users/{id}/reset-password", h.ResetPassword)
	app.Post("/admin/users/{id}/disable-2fa", h.DisableTwoFactor)

	id := strconv.Itoa(int(sa.ID))
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPatch, "/admin/users/" + id, `{"role":"admin"}`},
		{http.MethodPost, "/admin/users/" + id + "/reset-password", ""},
		{http.MethodPost, "/admin/users/" + id + "/disable-2fa", ""},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "service account") {
			t.Errorf("%s %s: status %d body %s, want 400 naming the service account", tc.method, tc.path, rec.Code, rec.Body)
		}
	}

	got, err := users.FindByID(sa.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != models.SystemRoleUser || got.PasswordHash != "!" {
		t.Errorf("service account changed: role %q, password hash %q", got.Role, got.PasswordHash)
	}
}
