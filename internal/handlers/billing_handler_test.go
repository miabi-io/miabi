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
	"github.com/miabi-io/miabi/internal/enterprise"
	errorhandlers "github.com/miabi-io/miabi/internal/error_handlers"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/auth"
	"github.com/miabi-io/miabi/internal/services/billing"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// licenceEE grants exactly the flags it holds, Require included.
type licenceEE struct{ fakeEE }

func (l licenceEE) Require(flag string) error {
	if l.flags[flag] {
		return nil
	}
	return enterprise.ErrEntitlementDenied
}

func TestBillingAPIState(t *testing.T) {
	if got := BillingAPIState(false, licenceEE{grant(enterprise.FlagBilling)}); got != BillingAPIOff {
		t.Errorf("switch off: %s", got)
	}
	if got := BillingAPIState(true, licenceEE{grant()}); got != BillingAPIUnlicensed {
		t.Errorf("no flag: %s", got)
	}
	if got := BillingAPIState(true, nil); got != BillingAPIUnlicensed {
		t.Errorf("no licence: %s", got)
	}
	if got := BillingAPIState(true, licenceEE{grant(enterprise.FlagBilling)}); got != BillingAPIOn {
		t.Errorf("on: %s", got)
	}
}

// TestBillingAPIAccess drives the real authentication path: only a billing key reaches the Billing
// API, a billing key reaches nothing else, the licence is checked per request, and a disabled group
// answers 404.
func TestBillingAPIAccess(t *testing.T) {
	dsn := os.Getenv("MIABI_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set MIABI_TEST_POSTGRES_DSN to run the Billing API access test")
	}
	schema := fmt.Sprintf("t_billing_%d", time.Now().UnixNano())
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
	if err := db.AutoMigrate(&models.User{}, &models.APIKey{}, &models.Plan{}, &models.BillingIdempotencyKey{}); err != nil {
		t.Fatal(err)
	}
	users := repositories.NewUserRepository(db)
	keys := auth.NewAPIKeyService(repositories.NewAPIKeyRepository(db))
	plans := repositories.NewPlanRepository(db)
	svc := billing.NewService(db, users, keys, plans, repositories.NewSettingRepository(db), billing.EnrolmentInfo{})
	if err := db.Create(&models.Plan{Name: "pro", IsActive: true, MaxApps: 30, AllowCustomTLS: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Plan{Name: "platform", IsActive: true, System: true}).Error; err != nil {
		t.Fatal(err)
	}

	billingKey, minted, err := svc.MintKey("console", nil)
	if err != nil || minted.Purpose() != models.KeyPurposeBilling {
		t.Fatalf("mint: %+v %v", minted, err)
	}
	again, err := svc.EnsureServiceUser()
	if err != nil || again.ID != minted.UserID || !again.IsService() || again.IsAdmin() {
		t.Fatalf("service user: %+v %v", again, err)
	}
	admin := &models.User{Name: "Admin", Username: "admin", Email: "admin@example.test", PasswordHash: "x", Role: models.SystemRoleAdmin, Active: true}
	if err := users.Create(admin); err != nil {
		t.Fatal(err)
	}
	allKey, _, err := keys.Create(admin.ID, nil, "automation", nil, []string{models.ScopeAll}, nil)
	if err != nil {
		t.Fatal(err)
	}

	ee := licenceEE{grant(enterprise.FlagBilling)}
	var licensed enterprise.EE = ee
	app := okapi.New()
	authn := middlewares.Authenticate(okapi.JWTAuth{}, keys, users, nil)
	v1 := app.Group("/api/v1")
	group := v1.Group("/billing")
	h := NewBillingHandler(svc, nil)
	group.Get("/plans", h.Plans, okapi.UseMiddleware(authn), okapi.UseMiddleware(middlewares.RequireBillingCaller()),
		okapi.UseMiddleware(func(c *okapi.Context) error { return middlewares.RequireLicensed(licensed, enterprise.FlagBilling)(c) }))
	v1.Get("/apps", func(c *okapi.Context) error { return c.JSON(http.StatusOK, map[string]string{"ok": "1"}) }, okapi.UseMiddleware(authn))

	call := func(path, key string) (int, []byte) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}

	code, body := call("/api/v1/billing/plans", billingKey)
	var out struct {
		Data []billing.Plan `json:"data"`
	}
	_ = json.Unmarshal(body, &out)
	if code != http.StatusOK || len(out.Data) != 1 || out.Data[0].Name != "pro" || out.Data[0].Limits["max_apps"] != 30 ||
		!out.Data[0].Capabilities["allow_custom_tls"] || len(out.Data[0].LimitsHash) != 64 {
		t.Fatalf("billing key on plans: %d %s", code, body)
	}
	for _, tc := range []struct {
		name, path, key string
		want            int
	}{
		{"billing key elsewhere", "/api/v1/apps", billingKey, http.StatusForbidden},
		{"all-powerful key on billing", "/api/v1/billing/plans", allKey, http.StatusForbidden},
		{"no key", "/api/v1/billing/plans", "", http.StatusUnauthorized},
		{"general key elsewhere", "/api/v1/apps", allKey, http.StatusOK},
	} {
		if code, body := call(tc.path, tc.key); code != tc.want {
			t.Errorf("%s: %d, want %d (%s)", tc.name, code, tc.want, body)
		}
	}

	licensed = licenceEE{grant()}
	if code, _ := call("/api/v1/billing/plans", billingKey); code != http.StatusForbidden {
		t.Errorf("licence without billing: %d, want 403 ENTITLEMENT_DENIED", code)
	}
	licensed = nil
	if code, _ := call("/api/v1/billing/plans", billingKey); code != http.StatusPaymentRequired {
		t.Errorf("no licence: %d, want 402", code)
	}

	licensed = ee
	group.Disable()
	if code, _ := call("/api/v1/billing/plans", billingKey); code != http.StatusNotFound {
		t.Errorf("disabled group: %d, want 404", code)
	}
}

// TestBillingIdempotentWrites drives account writes over HTTP: a retry with the same
// Idempotency-Key replays the stored answer, the same key on another request is refused, and
// 402s are not stored.
func TestBillingIdempotentWrites(t *testing.T) {
	dsn := os.Getenv("MIABI_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set MIABI_TEST_POSTGRES_DSN to run the Billing API write test")
	}
	schema := fmt.Sprintf("t_billing_writes_%d", time.Now().UnixNano())
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
	if err := db.AutoMigrate(&models.User{}, &models.APIKey{}, &models.Plan{}, &models.Workspace{}, &models.WorkspaceQuota{},
		&models.BillingAccount{}, &models.BillingIdempotencyKey{}, &models.Organization{}, &models.OAuthProvider{}, &models.UserIdentity{}, &models.BillingEvent{}); err != nil {
		t.Fatal(err)
	}
	users := repositories.NewUserRepository(db)
	keys := auth.NewAPIKeyService(repositories.NewAPIKeyRepository(db))
	svc := billing.NewService(db, users, keys, repositories.NewPlanRepository(db), repositories.NewSettingRepository(db), billing.EnrolmentInfo{})
	if err := db.Create(&models.Plan{Name: "pro", IsActive: true, MaxApps: 30}).Error; err != nil {
		t.Fatal(err)
	}
	billingKey, _, err := svc.MintKey("console", nil)
	if err != nil {
		t.Fatal(err)
	}

	app := okapi.New()
	app.WithErrorHandler(errorhandlers.CustomErrorHandler())
	g := app.Group("/api/v1/billing")
	mw := []okapi.RouteOption{okapi.UseMiddleware(middlewares.Authenticate(okapi.JWTAuth{}, keys, users, nil)), okapi.UseMiddleware(middlewares.RequireBillingCaller())}
	h := NewBillingHandler(svc, nil)
	g.Put("/accounts/{external_id}", okapi.H(h.UpsertAccount), mw...)
	g.Get("/accounts/{external_id}", okapi.H(h.GetAccount), mw...)
	g.Put("/accounts/{external_id}/subscription", okapi.H(h.PushSubscription), mw...)
	g.Patch("/accounts/{external_id}/workspaces/{workspace}", okapi.H(h.RenameWorkspace), mw...)
	g.Get("/accounts/{external_id}/usage", okapi.H(h.Usage), mw...)

	type answer struct {
		code     int
		replayed bool
		body     string
		errCode  string
	}
	call := func(method, path, key, body string) answer {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+billingKey)
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		var env struct {
			Error *struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		a := answer{code: rec.Code, replayed: rec.Header().Get("Idempotent-Replayed") == "true", body: rec.Body.String()}
		if env.Error != nil {
			a.errCode = env.Error.Code
		}
		return a
	}

	body := `{"email":"billing@acme.test","name":"Acme","owner":{"email":"jane@acme.test","name":"Jane"}}`
	first := call(http.MethodPut, "/api/v1/billing/accounts/cus_1", "k1", body)
	if first.code != http.StatusOK || first.replayed || !strings.Contains(first.body, `"external_id":"cus_1"`) {
		t.Fatalf("first upsert: %+v", first)
	}
	again := call(http.MethodPut, "/api/v1/billing/accounts/cus_1", "k1", body)
	if again.code != http.StatusOK || !again.replayed || again.body != first.body {
		t.Fatalf("replay: %+v", again)
	}
	if a := call(http.MethodPut, "/api/v1/billing/accounts/cus_1", "k1", strings.Replace(body, "Acme", "Other", 1)); a.code != http.StatusUnprocessableEntity || a.errCode != "IDEMPOTENCY_KEY_REUSED" {
		t.Errorf("reused key: %+v", a)
	}

	push := `{"external_id":"sub_1","plan":"pro","status":"active","event_at":"2026-10-23T09:00:00Z"}`
	if a := call(http.MethodPut, "/api/v1/billing/accounts/cus_1/subscription", "k2", push); a.code != http.StatusOK || !strings.Contains(a.body, `"plan":"pro"`) {
		t.Fatalf("push: %+v", a)
	}
	stale := `{"external_id":"sub_1","plan":"pro","status":"past_due","event_at":"2026-10-23T08:00:00Z"}`
	if a := call(http.MethodPut, "/api/v1/billing/accounts/cus_1/subscription", "k3", stale); a.code != http.StatusConflict || a.errCode != "BILLING_STALE_EVENT" || a.replayed {
		t.Fatalf("stale push: %+v", a)
	}
	if a := call(http.MethodPut, "/api/v1/billing/accounts/cus_1/subscription", "k3", stale); a.code != http.StatusConflict || !a.replayed {
		t.Errorf("a 409 is final and replayed: %+v", a)
	}
	unlicensed := `{"external_id":"sub_1","plan":"pro","status":"active","event_at":"2026-10-23T10:00:00Z","limits":{"max_apps":5}}`
	for i := 0; i < 2; i++ {
		if a := call(http.MethodPut, "/api/v1/billing/accounts/cus_1/subscription", "k4", unlicensed); a.code != http.StatusPaymentRequired || a.replayed {
			t.Errorf("402 attempt %d must run again, not replay: %+v", i, a)
		}
	}
	if a := call(http.MethodPut, "/api/v1/billing/accounts/cus_1/subscription?dry_run=true", "", push); a.code != http.StatusOK || !strings.Contains(a.body, `"workspaces":[]`) {
		t.Errorf("dry run: %+v", a)
	}
	if a := call(http.MethodGet, "/api/v1/billing/accounts/cus_404", "", ""); a.code != http.StatusNotFound || a.errCode != "BILLING_ACCOUNT_NOT_FOUND" {
		t.Errorf("missing account: %+v", a)
	}
	if a := call(http.MethodPatch, "/api/v1/billing/accounts/cus_1/workspaces/nope", "k5", `{"display_name":"X"}`); a.code != http.StatusNotFound || a.errCode != "BILLING_WORKSPACE_NOT_FOUND" {
		t.Errorf("rename an unknown workspace: %+v", a)
	}
	if a := call(http.MethodGet, "/api/v1/billing/accounts/cus_1/usage", "", ""); a.code != http.StatusOK || !strings.Contains(a.body, `"external_id":"cus_1"`) {
		t.Errorf("usage: %+v", a)
	}
}
