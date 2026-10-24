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
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/restriction"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

type fakeDeleter struct {
	db      *gorm.DB
	deleted []uint
}

func (f *fakeDeleter) DeleteWorkspaceNow(_ context.Context, wsID uint) error {
	f.deleted = append(f.deleted, wsID)
	return f.db.Delete(&models.Workspace{}, wsID).Error
}

func TestWorkspaceOperations(t *testing.T) {
	ctx := context.Background()
	db := accountsDB(t)
	wsRepo := repositories.NewWorkspaceRepository(db)
	restrictions := restriction.NewService(wsRepo, &countingWorkloads{})
	s := NewService(db, repositories.NewUserRepository(db), nil, repositories.NewPlanRepository(db), nil, EnrolmentInfo{})
	restrictions.SetBillingActor(s.ServiceUserID)
	deleter := &fakeDeleter{db: db}
	s.SetAccountDeps(AccountDeps{Workspaces: fakeCreator{db}, Meter: fakeMeter{"apps": 3}, Restrictions: restrictions, Deleter: deleter,
		EE: flagEE{flags: map[string]bool{enterprise.FlagQuotaOverride: true}}})
	pro := models.Plan{Name: "pro", IsActive: true, MaxCPUCores: 4}
	starter := models.Plan{Name: "starter", IsActive: true, MaxCPUCores: 1}
	db.Create(&pro)
	db.Create(&starter)
	isCode := func(err error, want *CodedError) bool {
		var ce *CodedError
		return errors.As(err, &ce) && ce.Code() == want.Code()
	}
	ws := func(name string) models.Workspace {
		var w models.Workspace
		db.Where("name = ?", name).First(&w)
		return w
	}

	two := 2
	upsert := AccountUpsert{Owner: Owner{Email: "jane@acme.test"}, CreateWorkspace: &CreateWorkspace{Name: "acme"}, MaxWorkspaces: &two}
	if _, err := s.UpsertAccount(ctx, "cus_1", upsert); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().Add(-time.Minute)
	if _, err := s.PushSubscription(ctx, "cus_1", SubscriptionPush{Plan: "pro", Status: "active", EventAt: &t0,
		Limits: map[string]int64{"max_cpu_cores": 2}}, false); err != nil {
		t.Fatal(err)
	}

	a, err := s.AddWorkspace(ctx, "cus_1", WorkspaceAttach{Create: &CreateWorkspace{Name: "acme-two", DisplayName: "Acme Two"}})
	if err != nil || len(a.Workspaces) != 2 || a.Workspaces[1].Plan != "pro" || a.Workspaces[1].DisplayName != "Acme Two" {
		t.Fatalf("create: %+v %v", a, err)
	}
	var q models.WorkspaceQuota
	db.First(&q, "workspace_id = ?", ws("acme-two").ID)
	if q.MaxCPUCores == nil || *q.MaxCPUCores != 6 {
		t.Fatalf("a new workspace takes the add-ons: %v", q.MaxCPUCores)
	}
	if a, err := s.AddWorkspace(ctx, "cus_1", WorkspaceAttach{Create: &CreateWorkspace{Name: "acme-two"}}); err != nil || len(a.Workspaces) != 2 {
		t.Fatalf("a retried create is answered, not repeated: %+v %v", a, err)
	}
	if _, err := s.AddWorkspace(ctx, "cus_1", WorkspaceAttach{Create: &CreateWorkspace{Name: "acme-three"}}); !isCode(err, ErrWorkspaceLimit) {
		t.Errorf("past max_workspaces: %v", err)
	}
	if _, err := s.AddWorkspace(ctx, "cus_1", WorkspaceAttach{}); !isCode(err, errAttachRequest) {
		t.Errorf("neither workspace nor create: %v", err)
	}

	three := 3
	upsert.MaxWorkspaces, upsert.CreateWorkspace = &three, nil
	if _, err := s.UpsertAccount(ctx, "cus_1", upsert); err != nil {
		t.Fatal(err)
	}
	jane := ws("acme").OwnerID
	if _, err := s.UpsertAccount(ctx, "cus_2", AccountUpsert{Owner: Owner{Email: "bob@acme.test"}, CreateWorkspace: &CreateWorkspace{Name: "bob"}}); err != nil {
		t.Fatal(err)
	}
	side := models.Workspace{Name: "side", OwnerID: jane, PlanID: &starter.ID}
	db.Create(&side)
	adminCPU := 9
	db.Create(&models.WorkspaceQuota{WorkspaceID: side.ID, MaxCPUCores: &adminCPU})

	if _, err := s.AddWorkspace(ctx, "cus_1", WorkspaceAttach{Workspace: "bob"}); !isCode(err, ErrWorkspaceAttached) {
		t.Errorf("billed elsewhere: %v", err)
	}
	db.Model(&models.Workspace{}).Where("name = ?", "bob").Update("billing_account_id", nil)
	if _, err := s.AddWorkspace(ctx, "cus_1", WorkspaceAttach{Workspace: "bob"}); !isCode(err, ErrWorkspaceNotOwned) {
		t.Errorf("someone else's workspace: %v", err)
	}
	if _, err := s.AddWorkspace(ctx, "cus_1", WorkspaceAttach{Workspace: side.UID}); !isCode(err, ErrOverrideConflict) {
		t.Errorf("admin override on a billed limit: %v", err)
	}
	db.Where("workspace_id = ?", side.ID).Delete(&models.WorkspaceQuota{})
	if a, err := s.AddWorkspace(ctx, "cus_1", WorkspaceAttach{Workspace: side.UID}); err != nil || len(a.Workspaces) != 3 {
		t.Fatalf("attach by uid: %+v %v", a, err)
	}
	if got := ws("side"); got.PlanID == nil || *got.PlanID != pro.ID || got.BillingPrevPlanID == nil || *got.BillingPrevPlanID != starter.ID {
		t.Fatalf("attach moves the plan and remembers the old one: %+v", got)
	}

	if a, err := s.RenameWorkspace("cus_1", "acme-two", WorkspaceRename{DisplayName: " Acme Prod "}); err != nil || a.Workspaces[1].DisplayName != "Acme Prod" || a.Workspaces[1].Slug != "acme-two" {
		t.Fatalf("rename: %+v %v", a, err)
	}
	if _, err := s.RenameWorkspace("cus_1", "acme-two", WorkspaceRename{DisplayName: " "}); !isCode(err, errDisplayName) {
		t.Errorf("empty name: %v", err)
	}
	if _, err := s.RenameWorkspace("cus_1", "bob", WorkspaceRename{DisplayName: "x"}); !isCode(err, ErrWorkspaceNotFound) {
		t.Errorf("another account's workspace: %v", err)
	}

	// Past grace, the account is read-only; detaching a workspace takes billing's hold off it.
	t1, grace := time.Now(), time.Now().Add(-time.Hour)
	if _, err := s.PushSubscription(ctx, "cus_1", SubscriptionPush{Plan: "pro", Status: "past_due", GraceUntil: &grace, EventAt: &t1,
		Limits: map[string]int64{"max_cpu_cores": 2}}, false); err != nil {
		t.Fatal(err)
	}
	if got := ws("side"); got.Restriction != models.WorkspaceRestrictionReadOnly {
		t.Fatalf("attached workspace follows the account: %s", got.Restriction)
	}
	if _, err := s.DetachWorkspace(ctx, "cus_1", "side"); err != nil {
		t.Fatal(err)
	}
	got := ws("side")
	if got.BillingAccountID != nil || got.PlanID == nil || *got.PlanID != starter.ID || got.Restriction != models.WorkspaceRestrictionNone {
		t.Fatalf("detach restores the plan and lifts billing's hold: %+v", got)
	}
	var n int64
	db.Model(&models.WorkspaceQuota{}).Where("workspace_id = ?", side.ID).Count(&n)
	if n != 0 {
		t.Fatal("detach must remove billing's add-ons")
	}

	if _, _, err := s.DeleteWorkspace(ctx, "cus_1", "acme-two", WorkspaceDelete{Confirm: "acme"}); !isCode(err, ErrConfirmMismatch) {
		t.Errorf("wrong confirm: %v", err)
	}
	if a, _, err := s.DeleteWorkspace(ctx, "cus_1", "acme-two", WorkspaceDelete{Confirm: "acme-two"}); err != nil || len(a.Workspaces) != 1 || len(deleter.deleted) != 1 {
		t.Fatalf("delete: %+v %v", a, err)
	}
	if _, _, err := s.DeleteWorkspace(ctx, "cus_1", "acme", WorkspaceDelete{Confirm: "acme"}); !isCode(err, ErrLastWorkspace) {
		t.Errorf("delete the last: %v", err)
	}
	if _, err := s.DetachWorkspace(ctx, "cus_1", "acme"); !isCode(err, ErrLastWorkspace) {
		t.Errorf("detach the last: %v", err)
	}

	u, err := s.AccountUsage("cus_1")
	if err != nil || len(u.Workspaces) != 1 || u.Workspaces[0].Used["apps"] != 3 || u.Workspaces[0].Limits == nil {
		t.Fatalf("usage: %+v %v", u, err)
	}

	if _, err := s.CancelAccount(ctx, "cus_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddWorkspace(ctx, "cus_1", WorkspaceAttach{Create: &CreateWorkspace{Name: "late"}}); !isCode(err, ErrAccountCanceled) {
		t.Errorf("add to a canceled account: %v", err)
	}
	if _, _, err := s.DeleteWorkspace(ctx, "cus_1", "acme", WorkspaceDelete{Confirm: "acme"}); err != nil {
		t.Errorf("a canceled account may lose its last workspace: %v", err)
	}
}
