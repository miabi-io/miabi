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

package workspace

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// fakeOrgs stands in for the organization service: the workspace service only needs a user's home
// realm and that realm's caps.
type fakeOrgs struct {
	org *models.Organization
}

func (f fakeOrgs) HomeOrganization(*models.User) uint {
	if f.org == nil {
		return 0
	}
	return f.org.ID
}
func (f fakeOrgs) CapReached(uint) bool { return false }
func (f fakeOrgs) Get(uint) (*models.Organization, error) {
	if f.org == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return f.org, nil
}

// seedOrg builds a service where user 1 owns `owned` workspaces, the platform default is `global`,
// and their organization carries `orgPerUser` (nil = inherit the platform default).
func seedOrg(t *testing.T, owned int, global int, orgPerUser *int, override *int, entitled bool) *Service {
	t.Helper()
	seedN++
	dsn := "file:orglimit" + string(rune('a'+seedN%26)) + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&wsRow{}, &memberRow{}, &userRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&userRow{ID: 1, WorkspaceLimit: override}).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < owned; i++ {
		ws := &wsRow{System: false}
		db.Create(ws)
		db.Create(&memberRow{WorkspaceID: ws.ID, UserID: 1, Role: string(models.WorkspaceRoleOwner)})
	}
	s := NewService(repositories.NewWorkspaceRepository(db), repositories.NewUserRepository(db), nil)
	s.SetLimits(func() int { return global }, func() bool { return entitled })
	s.SetOrgs(fakeOrgs{org: &models.Organization{ID: 7, MaxWorkspacesPerUser: orgPerUser}})
	// Organization-scoped limits are licensed here; TestOrgCapsAreInertWithoutTheLicence covers the
	// unlicensed install, where the platform defaults are the whole policy.
	s.SetOrgLimitsEntitled(func() bool { return true })
	return s
}

// The organization is the authority for its own tenants: its cap beats the platform default in both
// directions, tighter and looser.
func TestOrgPerUserCapBeatsThePlatformDefault(t *testing.T) {
	two := 2
	if err := seedOrg(t, 2, 10, &two, nil, false).canOwnAnother(1); err == nil {
		t.Error("org cap of 2 with 2 owned: want refusal even though the platform default allows 10")
	}

	twenty := 20
	if err := seedOrg(t, 5, 3, &twenty, nil, false).canOwnAnother(1); err != nil {
		t.Errorf("org cap of 20 over a platform default of 3: got %v, want nil", err)
	}

	unlimited := models.Unlimited
	if err := seedOrg(t, 99, 3, &unlimited, nil, false).canOwnAnother(1); err != nil {
		t.Errorf("org unlimited: got %v, want nil", err)
	}

	none := 0
	if err := seedOrg(t, 0, 10, &none, nil, false).canOwnAnother(1); err == nil {
		t.Error("org cap of 0 must allow none")
	}
}

// An org that sets nothing inherits the platform default, which is what keeps a single-org install
// behaving exactly as it did before the limit moved.
func TestOrgWithoutACapInheritsThePlatformDefault(t *testing.T) {
	if err := seedOrg(t, 3, 3, nil, nil, false).canOwnAnother(1); err == nil {
		t.Error("inherited platform cap of 3 with 3 owned: want refusal")
	}
	if err := seedOrg(t, 2, 3, nil, nil, false).canOwnAnother(1); err != nil {
		t.Errorf("under the inherited cap: got %v, want nil", err)
	}
}

// The Enterprise per-user override is the finest grain, so it outranks the organization.
func TestPerUserOverrideBeatsTheOrgCap(t *testing.T) {
	one := 1
	twenty := 20
	if err := seedOrg(t, 1, 20, &twenty, &one, true).canOwnAnother(1); err == nil {
		t.Error("override of 1 with 1 owned: want refusal despite the org allowing 20")
	}
	// Unlicensed, the override is ignored and the org decides.
	if err := seedOrg(t, 1, 20, &twenty, &one, false).canOwnAnother(1); err != nil {
		t.Errorf("override without the entitlement: got %v, want nil (org allows 20)", err)
	}
}

// Without organizations wired, the platform default is in sole charge.
func TestUnwiredOrgsLeaveThePlatformDefaultInCharge(t *testing.T) {
	s := seedOrg(t, 3, 3, nil, nil, false)
	s.SetOrgs(nil)
	if err := s.canOwnAnother(1); err == nil {
		t.Error("platform cap of 3 with 3 owned and no orgs: want refusal")
	}
}

// Without the organizations licence an org's stored caps are ignored and the platform default
// decides. An operator who cannot edit those caps must not be bound by them — which is also what
// keeps a lapsed licence from stranding a tenant.
func TestOrgCapsAreInertWithoutTheLicence(t *testing.T) {
	two := 2
	s := seedOrg(t, 5, 10, &two, nil, false)
	s.SetOrgLimitsEntitled(func() bool { return false })
	if err := s.canOwnAnother(1); err != nil {
		t.Errorf("unlicensed org cap of 2 with 5 owned: got %v, want nil (platform default of 10 applies)", err)
	}

	// And the platform default still binds on its own.
	s = seedOrg(t, 10, 10, &two, nil, false)
	s.SetOrgLimitsEntitled(func() bool { return false })
	if err := s.canOwnAnother(1); err == nil {
		t.Error("platform default of 10 with 10 owned: want refusal")
	}

	// An unlimited org cap must not lift the platform default either.
	unlimited := models.Unlimited
	s = seedOrg(t, 10, 10, &unlimited, nil, false)
	s.SetOrgLimitsEntitled(func() bool { return false })
	if err := s.canOwnAnother(1); err == nil {
		t.Error("unlicensed org unlimited: want the platform default to still refuse")
	}
}

// withOwnerCap gives user 1's organization a per-user cap and an owner cap, owned by ownerID.
func withOwnerCap(s *Service, ownerID uint, perUser, perOwner *int) *Service {
	s.SetOrgs(fakeOrgs{org: &models.Organization{ID: 7, OwnerUserID: ownerID, MaxWorkspacesPerUser: perUser, MaxWorkspacesPerOwner: perOwner}})
	return s
}

func TestOwnerCapReplacesThePerUserCapForTheOwner(t *testing.T) {
	two, unlimited, five := 2, models.Unlimited, 5
	if err := withOwnerCap(seedOrg(t, 9, 10, nil, nil, true), 1, &two, &unlimited).canOwnAnother(1); err != nil {
		t.Errorf("owner with an unlimited owner cap over a per-user cap of 2: got %v, want nil", err)
	}
	if err := withOwnerCap(seedOrg(t, 5, 10, nil, nil, true), 1, &two, &five).canOwnAnother(1); err == nil {
		t.Error("owner cap of 5 with 5 owned: want refusal")
	}
	if err := withOwnerCap(seedOrg(t, 2, 10, nil, nil, true), 1, &two, nil).canOwnAnother(1); err == nil {
		t.Error("no owner cap: the per-user cap of 2 must bind the owner too")
	}
}

func TestOwnerCapDoesNotApplyToOtherUsers(t *testing.T) {
	two, unlimited := 2, models.Unlimited
	if err := withOwnerCap(seedOrg(t, 2, 10, nil, nil, true), 99, &two, &unlimited).canOwnAnother(1); err == nil {
		t.Error("user 1 is not the owner: want the per-user cap of 2 to refuse")
	}
}

// The owner cap is Enterprise (user_workspace_limit); without it the owner is an ordinary user.
func TestOwnerCapIsInertWithoutTheEntitlement(t *testing.T) {
	two, unlimited := 2, models.Unlimited
	if err := withOwnerCap(seedOrg(t, 2, 10, nil, nil, false), 1, &two, &unlimited).canOwnAnother(1); err == nil {
		t.Error("unlicensed owner cap: want the per-user cap of 2 to refuse")
	}
}

// A per-user override on the owner is the finest grain, so it still outranks the owner cap.
func TestPerUserOverrideBeatsTheOwnerCap(t *testing.T) {
	one, unlimited := 1, models.Unlimited
	if err := withOwnerCap(seedOrg(t, 1, 10, nil, &one, true), 1, nil, &unlimited).canOwnAnother(1); err == nil {
		t.Error("override of 1 with 1 owned: want refusal despite the unlimited owner cap")
	}
}
