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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/account"
	"github.com/miabi-io/miabi/internal/services/restriction"
	"github.com/miabi-io/miabi/internal/services/workspace"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// TestAdminWorkspaceLifecycle drives the admin API an external system uses: create a workspace for
// a new owner, refuse a taken name, make it read-only (members' writes refused, reads and the
// exempt writes still accepted), then lift it.
func TestAdminWorkspaceLifecycle(t *testing.T) {
	dsn := os.Getenv("MIABI_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set MIABI_TEST_POSTGRES_DSN to run the admin workspace API test")
	}
	schema := fmt.Sprintf("t_admin_ws_%d", time.Now().UnixNano())
	root, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Exec("DROP SCHEMA " + schema + " CASCADE") })
	db, err := gorm.Open(postgres.Open(dsn+" search_path="+schema), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.Workspace{}, &models.WorkspaceMember{}, &models.Plan{},
		&models.Application{}, &models.DatabaseInstance{}); err != nil {
		t.Fatal(err)
	}
	users := repositories.NewUserRepository(db)
	wsRepo := repositories.NewWorkspaceRepository(db)
	acct := account.NewService(users, wsRepo, repositories.NewApplicationRepository(db), repositories.NewDatabaseRepository(db), nil, nil, nil, nil, nil)
	restrictions := restriction.NewService(wsRepo, acct)
	h := NewAdminWorkspaceOpsHandler(wsRepo, users, repositories.NewPlanRepository(db), workspace.NewService(wsRepo, users, nil), acct, restrictions, nil)

	app := okapi.New()
	asUser := func(id uint) okapi.Middleware {
		return func(c *okapi.Context) error {
			c.Set(middlewares.CtxUserID, int(id))
			return c.Next()
		}
	}
	admin := okapi.UseMiddleware(asUser(1))
	app.Post("/admin/workspaces", okapi.H(h.Create), admin)
	app.Post("/admin/workspaces/{id}/restriction", okapi.H(h.SetRestriction), admin)
	app.Delete("/admin/workspaces/{id}/restriction", h.LiftRestriction, admin)

	call := func(method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		var out struct {
			Data map[string]any `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out.Data
	}

	code, data := call(http.MethodPost, "/admin/workspaces", `{"name":"acme-prod","display_name":"Acme","owner_email":"Ada@Acme.example","owner_name":"Ada"}`)
	if code != http.StatusCreated || data["owner_created"] != true {
		t.Fatalf("create: %d %v", code, data)
	}
	owner, err := users.FindByEmail("ada@acme.example")
	if err != nil || owner.PasswordHash == "" || owner.EmailVerifiedAt == nil {
		t.Fatalf("owner account: %+v %v", owner, err)
	}
	if code, _ := call(http.MethodPost, "/admin/workspaces", `{"name":"acme-prod","owner_email":"other@acme.example"}`); code != http.StatusConflict {
		t.Fatalf("taken name: %d, want 409", code)
	}
	if _, err := users.FindByEmail("other@acme.example"); err == nil {
		t.Fatal("a refused name must not leave an owner account behind")
	}

	// The workspace-scoped API a member uses, behind the same gate as production.
	scope := okapi.UseMiddleware(middlewares.WorkspaceScope(wsRepo, nil, restrictions))
	member := okapi.UseMiddleware(asUser(owner.ID))
	okHandler := func(c *okapi.Context) error { return c.JSON(http.StatusOK, map[string]string{"ok": "1"}) }
	app.Get("/api/v1/workspaces/{workspace}/apps", okHandler, member, scope)
	app.Post("/api/v1/workspaces/{workspace}/apps", okHandler, member, scope)
	app.Delete("/api/v1/workspaces/{workspace}/members/{userID}", okHandler, member, scope)

	if code, _ := call(http.MethodPost, "/admin/workspaces/acme-prod/restriction", `{"state":"read_only","reason":"invoice unpaid"}`); code != http.StatusOK {
		t.Fatalf("restrict by name: %d", code)
	}
	for _, tt := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/api/v1/workspaces/acme-prod/apps", http.StatusOK},
		{http.MethodPost, "/api/v1/workspaces/acme-prod/apps", http.StatusForbidden},
		{http.MethodDelete, "/api/v1/workspaces/acme-prod/members/9", http.StatusOK},
	} {
		if code, _ := call(tt.method, tt.path, "{}"); code != tt.want {
			t.Errorf("read-only %s %s: %d, want %d", tt.method, tt.path, code, tt.want)
		}
	}
	if code, _ := call(http.MethodDelete, "/admin/workspaces/acme-prod/restriction", ""); code != http.StatusOK {
		t.Fatalf("lift: %d", code)
	}
	if code, _ := call(http.MethodPost, "/api/v1/workspaces/acme-prod/apps", "{}"); code != http.StatusOK {
		t.Fatalf("write after lift: %d", code)
	}
}
