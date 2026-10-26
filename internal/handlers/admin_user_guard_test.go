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
	"errors"
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

// A guarded account must come out unchanged, not just get a 409: AbortWithError returns nil once
// the response is written, so a guard that hands its result back as an error lets the change through.
func TestUserGuardBlocksTheChange(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.User{}); err != nil {
		t.Fatal(err)
	}
	users := repositories.NewUserRepository(db)
	u := &models.User{Name: "Billing", Username: "billing-service", Email: "billing@example.invalid", PasswordHash: "!",
		Role: models.SystemRoleUser, Active: true, Kind: models.UserKindService}
	if err := users.Create(u); err != nil {
		t.Fatal(err)
	}

	h := NewAdminUserHandler(db, users, nil, nil, nil, nil, nil, nil, 0)
	h.SetUserGuard(func(*models.User) error { return errors.New("locked") })
	app := okapi.New()
	app.Patch("/admin/users/{id}", okapi.H(h.Update))
	app.Post("/admin/users/{id}/suspension", okapi.H(h.Suspend))

	id := strconv.Itoa(int(u.ID))
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPatch, "/admin/users/" + id, `{"active":false}`},
		{http.MethodPost, "/admin/users/" + id + "/suspension", `{"reason":"test"}`},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict {
			t.Errorf("%s %s: status %d, want 409", tc.method, tc.path, rec.Code)
		}
	}
	got, err := users.FindByID(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Active || got.SuspendedAt != nil {
		t.Errorf("guarded account changed: active=%v suspended_at=%v", got.Active, got.SuspendedAt)
	}
}
