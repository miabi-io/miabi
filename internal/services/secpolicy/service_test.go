// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package secpolicy

import (
	"errors"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeEE struct{ on bool }

func (f fakeEE) Has(string) bool { return f.on }

type noWorkspaces struct{}

func (noWorkspaces) FindByID(uint) (*models.Workspace, error) { return nil, gorm.ErrRecordNotFound }

func newTestService(t *testing.T, entitled, enabled bool) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.SecurityPolicy{}, &models.SecurityEvent{}); err != nil {
		t.Fatal(err)
	}
	return NewService(repositories.NewSecurityPolicyRepository(db), noWorkspaces{}, fakeEE{entitled}, enabled), db
}

func TestSaveRefusesLooserOverrideAndRecordsChanges(t *testing.T) {
	svc, db := newTestService(t, true, true)
	if _, err := svc.Save(1, "", SaveInput{Kind: KindPorts, ScopeType: ScopePlatform, Mode: ModeEnforce, Spec: `{"mode":"reject_all"}`}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Save(1, "", SaveInput{Kind: KindPorts, ScopeType: ScopeWorkspace, ScopeID: 5, Mode: ModeEnforce, Spec: `{"mode":"approval"}`})
	if !errors.Is(err, ErrLoosens) {
		t.Fatalf("err = %v, want ErrLoosens", err)
	}
	var n int64
	db.Model(&models.SecurityEvent{}).Where("decision = ?", DecisionChange).Count(&n)
	if n != 1 {
		t.Errorf("recorded %d change events, want 1", n)
	}
	if _, err := svc.Save(1, "", SaveInput{Kind: KindPorts, ScopeType: ScopeWorkspace, ScopeID: 5, Mode: ModeEnforce, AllowExceptions: true, Spec: `{"mode":"reject_all"}`}); !errors.Is(err, ErrInvalidSpec) {
		t.Errorf("a workspace rule allowing exceptions was accepted: %v", err)
	}
}

func TestEnforcedRejectAllDeniesAndRecords(t *testing.T) {
	svc, db := newTestService(t, true, true)
	if _, err := svc.Save(1, "", SaveInput{Kind: KindPorts, ScopeType: ScopePlatform, Mode: ModeEnforce, Spec: `{"mode":"reject_all"}`}); err != nil {
		t.Fatal(err)
	}
	d := svc.CheckPortRequest(PortRequest{WorkspaceID: 3, UserID: 2, HostPort: 30001, Action: "request"})
	if d.Allowed {
		t.Fatal("reject_all allowed a request")
	}
	var ev models.SecurityEvent
	if err := db.Where("kind = ? AND decision = ?", KindPorts, DecisionDeny).First(&ev).Error; err != nil {
		t.Fatalf("no deny event recorded: %v", err)
	}
	if ev.Scope != ScopePlatform || ev.WorkspaceID == nil || *ev.WorkspaceID != 3 {
		t.Errorf("event = %+v", ev)
	}
}

// Without the entitlement, or with the kill switch off, stored rules are kept but not applied.
func TestInactiveServiceKeepsCommunityBehaviour(t *testing.T) {
	for _, tc := range []struct {
		name              string
		entitled, enabled bool
	}{{"community", false, true}, {"kill switch", true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			svc, db := newTestService(t, tc.entitled, tc.enabled)
			db.Create(&models.SecurityPolicy{Kind: KindPorts, ScopeType: ScopePlatform, Mode: ModeEnforce, Spec: `{"mode":"reject_all"}`})
			d := svc.CheckPortRequest(PortRequest{WorkspaceID: 3, HostPort: 30001, Privileged: true})
			if !d.Allowed || !d.AutoApprove {
				t.Fatalf("decision = %+v, want Community behaviour", d)
			}
			if pd := svc.CheckPortPublish(PortPublish{Binding: models.PortBinding{UpdatedAt: time.Now().Add(time.Hour)}}); !pd.Allowed {
				t.Fatal("an inactive policy blocked a publish")
			}
		})
	}
}

func TestAdminAdoptedBindingIsExempt(t *testing.T) {
	b := models.PortBinding{HostPort: 8080, Protocol: "tcp", AdminAdopted: true, UpdatedAt: time.Now().Add(time.Hour)}
	if d, denied := decidePublish(ModeEnforce, PortsSpec{Mode: PortsRejectAll}, time.Now(), b); !d.Allowed || denied != "" {
		t.Fatalf("an admin-adopted binding was refused: %+v %q", d, denied)
	}
}
