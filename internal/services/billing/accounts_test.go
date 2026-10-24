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

package billing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/workspace"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestDerivedLevel(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	for _, tc := range []struct {
		status string
		grace  *time.Time
		want   string
	}{
		{"active", nil, models.BillingLevelNone},
		{"trialing", nil, models.BillingLevelNone},
		{"", nil, models.BillingLevelNone},
		{"past_due", nil, models.BillingLevelWarning},
		{"past_due", &future, models.BillingLevelWarning},
		{"past_due", &past, models.BillingLevelSoft},
		{"unpaid", &past, models.BillingLevelSoft},
		{"incomplete", &now, models.BillingLevelSoft},
		{"paused", nil, models.BillingLevelSoft},
		{"canceled", nil, models.BillingLevelHard},
		{"incomplete_expired", nil, models.BillingLevelHard},
	} {
		if got := DerivedLevel(tc.status, tc.grace, now, 0); got != tc.want {
			t.Errorf("%s grace=%v: %s, want %s", tc.status, tc.grace, got, tc.want)
		}
	}

	longAgo := now.Add(-15 * 24 * time.Hour)
	if got := DerivedLevel("unpaid", &longAgo, now, 14*24*time.Hour); got != models.BillingLevelHard {
		t.Errorf("unpaid past grace + hard-after: %s", got)
	}
	if got := DerivedLevel("unpaid", &past, now, 14*24*time.Hour); got != models.BillingLevelSoft {
		t.Errorf("unpaid just past grace: %s", got)
	}

	a := &models.BillingAccount{Status: models.BillingAccountActive, SubStatus: "past_due", ForcedRestriction: models.BillingLevelSoft}
	if got := EffectiveLevel(a, now, 0); got != models.BillingLevelSoft {
		t.Errorf("forced soft over warning: %s", got)
	}
	a.ForcedUntil = &past
	if got := EffectiveLevel(a, now, 0); got != models.BillingLevelWarning {
		t.Errorf("expired forced hold: %s", got)
	}
	a.ForcedRestriction, a.ForcedUntil, a.SubStatus = models.BillingLevelWarning, nil, "canceled"
	if got := EffectiveLevel(a, now, 0); got != models.BillingLevelHard {
		t.Errorf("forced below derived: %s", got)
	}
	a.SubStatus, a.Status = "active", models.BillingAccountCanceled
	if got := EffectiveLevel(a, now, 0); got != models.BillingLevelHard {
		t.Errorf("canceled account: %s", got)
	}
}

func TestWithDelta(t *testing.T) {
	for _, tc := range []struct{ base, delta, want int64 }{
		{4, 2, 6}, {-1, 2, -1}, {4, -6, 0}, {0, 3, 3},
	} {
		if got := withDelta(tc.base, tc.delta); got != tc.want {
			t.Errorf("withDelta(%d, %d) = %d, want %d", tc.base, tc.delta, got, tc.want)
		}
	}
	if err := validateLimits(map[string]int64{"max_cpu_cores": 1, "max_memory_mb": 512}); err != nil {
		t.Errorf("known limits refused: %v", err)
	}
	if err := validateLimits(map[string]int64{"allow_shell_exec": 1}); !errors.Is(err, ErrUnknownLimit) {
		t.Errorf("capability as a limit: %v", err)
	}
}

type fakeCreator struct{ db *gorm.DB }

func (f fakeCreator) CreateForOwner(ownerID uint, displayName, handle string) (*models.Workspace, error) {
	var n int64
	f.db.Model(&models.Workspace{}).Where("name = ?", handle).Count(&n)
	if n > 0 {
		return nil, workspace.ErrNameTaken
	}
	if displayName == "" {
		displayName = handle
	}
	ws := &models.Workspace{Name: handle, DisplayName: displayName, OwnerID: ownerID}
	return ws, f.db.Create(ws).Error
}

type fakeMeter map[string]float64

func (m fakeMeter) Measure(uint) map[string]float64 { return m }

func (m fakeMeter) Limits(uint) any { return map[string]int{"max_apps": 30} }

type flagEE struct {
	enterprise.EE
	flags map[string]bool
}

func (f flagEE) Require(flag string) error {
	if f.flags[flag] {
		return nil
	}
	return enterprise.ErrEntitlementDenied
}

func accountsDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("MIABI_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set MIABI_TEST_POSTGRES_DSN to run the billing account tests")
	}
	schema := fmt.Sprintf("t_billing_acct_%d", time.Now().UnixNano())
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
	if err := db.AutoMigrate(&models.User{}, &models.Plan{}, &models.Workspace{}, &models.WorkspaceQuota{}, &models.BillingAccount{},
		&models.Organization{}, &models.OAuthProvider{}, &models.UserIdentity{}, &models.BillingEvent{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestAccounts(t *testing.T) {
	ctx := context.Background()
	db := accountsDB(t)
	plans := repositories.NewPlanRepository(db)
	s := NewService(db, repositories.NewUserRepository(db), nil, plans, nil, EnrolmentInfo{MiabiURL: "https://paas.example.com/"})
	var invited []string
	meter := fakeMeter{"apps": 12, "cpu_cores": 1.5}
	s.SetAccountDeps(AccountDeps{Workspaces: fakeCreator{db}, Meter: meter, OwnerCreated: func(u *models.User) { invited = append(invited, u.Email) }})
	for _, p := range []*models.Plan{
		{Name: "pro", IsActive: true, MaxApps: 30, MaxCPUCores: 4, MaxMemoryMB: 8192},
		{Name: "starter", IsActive: true, MaxApps: 10, MaxCPUCores: 1},
		{Name: "platform", IsActive: true, System: true},
	} {
		if err := db.Create(p).Error; err != nil {
			t.Fatal(err)
		}
	}
	isCode := func(err error, want *CodedError) bool {
		var ce *CodedError
		return errors.As(err, &ce) && ce.Code() == want.Code() && ce.Status == want.Status
	}

	upsert := AccountUpsert{Email: "billing@acme.test", Name: "Acme", Owner: Owner{Email: "Jane@Acme.test", Name: "Jane"},
		CreateWorkspace: &CreateWorkspace{Name: "acme"}}
	a, err := s.UpsertAccount(ctx, "cus_1", upsert)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Workspaces) != 1 || a.Workspaces[0].Slug != "acme" || a.Workspaces[0].DashboardURL != "https://paas.example.com/?workspace=acme" ||
		a.Status != models.BillingAccountActive || a.Subscription != nil || a.MaxWorkspaces != models.Unlimited {
		t.Fatalf("first upsert: %+v", a)
	}
	if len(invited) != 1 || invited[0] != "jane@acme.test" {
		t.Fatalf("set-password mail for the new owner: %v", invited)
	}
	var owner models.User
	db.Where("email = ?", "jane@acme.test").First(&owner)
	if owner.SignupSource != models.SignupSourceBilling || owner.EmailVerifiedAt == nil || !owner.Active {
		t.Fatalf("owner: %+v", owner)
	}

	two := 2
	upsert.Name, upsert.MaxWorkspaces, upsert.CreateWorkspace = "Acme SARL", &two, &CreateWorkspace{Name: "acme-two"}
	if a, err = s.UpsertAccount(ctx, "cus_1", upsert); err != nil || a.Name != "Acme SARL" || a.MaxWorkspaces != 2 || len(a.Workspaces) != 1 {
		t.Fatalf("second upsert must update and not create another workspace: %+v %v", a, err)
	}
	if len(invited) != 1 {
		t.Fatalf("an existing owner gets no mail: %v", invited)
	}
	changed := upsert
	changed.Owner.Email = "someone@acme.test"
	if _, err := s.UpsertAccount(ctx, "cus_1", changed); !isCode(err, ErrOwnerChange) {
		t.Errorf("owner change: %v", err)
	}

	admin := models.User{Name: "Admin", Username: "root", Email: "root@acme.test", PasswordHash: "x", Role: models.SystemRoleAdmin, Active: true}
	inactive := models.User{Name: "Gone", Username: "gone", Email: "gone@acme.test", PasswordHash: "x", Role: models.SystemRoleUser, Active: true}
	db.Create(&admin)
	db.Create(&inactive)
	db.Model(&inactive).Update("active", false)
	for _, tc := range []struct {
		email string
		want  *CodedError
	}{
		{"root@acme.test", ErrOwnerProtected},
		{"gone@acme.test", ErrOwnerInactive},
		{"not-an-email", ErrOwnerInvalid},
	} {
		if _, err := s.UpsertAccount(ctx, "cus_"+tc.email, AccountUpsert{Owner: Owner{Email: tc.email}}); !isCode(err, tc.want) {
			t.Errorf("owner %s: %v, want %s", tc.email, err, tc.want.Code())
		}
	}
	if _, err := s.UpsertAccount(ctx, "cus_x", AccountUpsert{Owner: Owner{Email: "x@acme.test"}, Organization: "nope"}); !isCode(err, ErrUnknownOrg) {
		t.Errorf("unknown organization: %v", err)
	}

	t0 := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	push := SubscriptionPush{ExternalID: "sub_1", Plan: "pro", Status: "active", EventAt: &t0}
	for _, tc := range []struct {
		name string
		edit func(p *SubscriptionPush)
		want *CodedError
	}{
		{"unknown status", func(p *SubscriptionPush) { p.Status = "gone" }, ErrUnknownStatus},
		{"unknown plan", func(p *SubscriptionPush) { p.Plan = "enterprise" }, ErrUnknownPlan},
		{"system plan", func(p *SubscriptionPush) { p.Plan = "platform" }, ErrUnknownPlan},
		{"unknown limit", func(p *SubscriptionPush) { p.Limits = map[string]int64{"max_widgets": 1} }, ErrUnknownLimit},
		{"limits without licence", func(p *SubscriptionPush) { p.Limits = map[string]int64{"max_cpu_cores": 2} }, ErrLimitsUnlicensed},
	} {
		p := push
		tc.edit(&p)
		if _, err := s.PushSubscription(ctx, "cus_1", p, false); !isCode(err, tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	if _, err := s.PushSubscription(ctx, "cus_404", push, false); !isCode(err, ErrAccountNotFound) {
		t.Errorf("missing account: %v", err)
	}

	s.deps.EE = flagEE{flags: map[string]bool{enterprise.FlagQuotaOverride: true}}
	var ws models.Workspace
	db.Where("name = ?", "acme").First(&ws)
	adminSet := 7
	db.Create(&models.WorkspaceQuota{WorkspaceID: ws.ID, MaxVolumes: &adminSet})

	push.Limits = map[string]int64{"max_cpu_cores": 2, "max_memory_mb": 4096}
	res, err := s.PushSubscription(ctx, "cus_1", push, false)
	if err != nil || !res.Changed || res.Account.Subscription == nil || res.Account.Subscription.Plan != "pro" ||
		res.Account.Workspaces[0].Plan != "pro" || res.Account.RestrictionLevel != models.BillingLevelNone {
		t.Fatalf("push: %+v %v", res, err)
	}
	var q models.WorkspaceQuota
	db.First(&q, "workspace_id = ?", ws.ID)
	if q.MaxCPUCores == nil || *q.MaxCPUCores != 6 || q.MaxMemoryMB == nil || *q.MaxMemoryMB != 12288 || q.MaxVolumes == nil || *q.MaxVolumes != 7 {
		t.Fatalf("override after push: cpu=%v mem=%v volumes=%v", q.MaxCPUCores, q.MaxMemoryMB, q.MaxVolumes)
	}

	if res, err := s.PushSubscription(ctx, "cus_1", push, false); err != nil || res.Changed {
		t.Errorf("identical resend must be a no-op: %+v %v", res, err)
	}
	other := push
	other.Status = "past_due"
	if _, err := s.PushSubscription(ctx, "cus_1", other, false); !isCode(err, ErrStaleEvent) {
		t.Errorf("same event_at, different state: %v", err)
	}
	older := t0.Add(-time.Minute)
	other.EventAt = &older
	if _, err := s.PushSubscription(ctx, "cus_1", other, false); !isCode(err, ErrStaleEvent) {
		t.Errorf("older event_at: %v", err)
	}

	t1, grace := t0.Add(time.Hour), time.Now().Add(-time.Minute)
	down := SubscriptionPush{ExternalID: "sub_1", Plan: "starter", Status: "past_due", GraceUntil: &grace, EventAt: &t1,
		Limits: map[string]int64{"max_cpu_cores": 1}}
	dry, err := s.PushSubscription(ctx, "cus_1", down, true)
	if err != nil || dry.DryRun == nil || dry.DryRun.Plan != "starter" || len(dry.DryRun.Workspaces) != 1 {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	if v := dry.DryRun.Workspaces[0].Violations; len(v) != 1 || v[0].Resource != "max_apps" || v[0].Used != 12 || v[0].Limit != 10 {
		t.Fatalf("violations: %+v", v)
	}
	if got, _ := s.GetAccount("cus_1"); got.Subscription.Plan != "pro" {
		t.Fatalf("a dry run must not apply: %+v", got.Subscription)
	}

	res, err = s.PushSubscription(ctx, "cus_1", down, false)
	if err != nil || res.Account.RestrictionLevel != models.BillingLevelSoft || res.Account.Workspaces[0].Plan != "starter" {
		t.Fatalf("downgrade past grace: %+v %v", res, err)
	}
	q = models.WorkspaceQuota{}
	db.First(&q, "workspace_id = ?", ws.ID)
	if q.MaxCPUCores == nil || *q.MaxCPUCores != 2 || q.MaxMemoryMB != nil || q.MaxVolumes == nil || *q.MaxVolumes != 7 {
		t.Fatalf("override after downgrade: cpu=%v mem=%v volumes=%v", q.MaxCPUCores, q.MaxMemoryMB, q.MaxVolumes)
	}

	a, err = s.CancelAccount(ctx, "cus_1")
	if err != nil || a.Status != models.BillingAccountCanceled || a.RestrictionLevel != models.BillingLevelHard ||
		a.Subscription.Status != "canceled" || len(a.Workspaces) != 1 {
		t.Fatalf("cancel: %+v %v", a, err)
	}
	if again, err := s.CancelAccount(ctx, "cus_1"); err != nil || again.Status != models.BillingAccountCanceled {
		t.Fatalf("cancel twice: %+v %v", again, err)
	}
	t2 := t1.Add(time.Hour)
	down.EventAt = &t2
	if _, err := s.PushSubscription(ctx, "cus_1", down, false); !isCode(err, ErrAccountCanceled) {
		t.Errorf("push to a canceled account: %v", err)
	}
	upsert.CreateWorkspace = nil
	if a, err = s.UpsertAccount(ctx, "cus_1", upsert); err != nil || a.Status != models.BillingAccountActive {
		t.Fatalf("upsert reopens: %+v %v", a, err)
	}

	// A workspace an earlier attempt created but never bound is taken over, not refused.
	if _, err := s.UpsertAccount(ctx, "cus_2", AccountUpsert{Owner: Owner{Email: "bob@acme.test"}}); err != nil {
		t.Fatal(err)
	}
	var bob models.User
	db.Where("email = ?", "bob@acme.test").First(&bob)
	db.Create(&models.Workspace{Name: "bob", OwnerID: bob.ID})
	if a, err := s.UpsertAccount(ctx, "cus_2", AccountUpsert{Owner: Owner{Email: "bob@acme.test"}, CreateWorkspace: &CreateWorkspace{Name: "bob"}}); err != nil || len(a.Workspaces) != 1 {
		t.Fatalf("take over own unbound workspace: %+v %v", a, err)
	}
	if _, err := s.UpsertAccount(ctx, "cus_3", AccountUpsert{Owner: Owner{Email: "eve@acme.test"}, CreateWorkspace: &CreateWorkspace{Name: "bob"}}); !isCode(err, ErrWorkspaceTaken) {
		t.Errorf("someone else's workspace name: %v", err)
	}
}
