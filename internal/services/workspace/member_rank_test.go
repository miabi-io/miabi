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
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// The leaner memberRow in limit_test.go omits the columns UpdateMemberRole writes.
type memberRankRow struct {
	ID           uint `gorm:"primaryKey"`
	WorkspaceID  uint
	UserID       uint
	Role         string
	CustomRoleID *uint
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (memberRankRow) TableName() string { return "workspace_members" }

// A second owner (user 99) keeps guardLastOwner from masking a rank failure with ErrLastOwner.
func seedMembers(t *testing.T, members map[uint]models.WorkspaceRole) *Service {
	t.Helper()
	seedN++
	dsn := fmt.Sprintf("file:wsrank%d?mode=memory&cache=shared", seedN)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&wsRow{}, &memberRankRow{}, &userRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.Create(&wsRow{ID: 1})
	for uid, role := range members {
		db.Create(&userRow{ID: uid})
		db.Create(&memberRankRow{WorkspaceID: 1, UserID: uid, Role: string(role)})
	}
	s := NewService(repositories.NewWorkspaceRepository(db), repositories.NewUserRepository(db), nil)
	s.SetLimits(func() int { return 0 }, func() bool { return false })
	return s
}

const (
	uOwner  uint = 10 // an owner
	uOwner2 uint = 99 // a second owner, so the last-owner guard stays out of the way
	uAdmin  uint = 20
	uDev    uint = 30
)

func fullWorkspace(t *testing.T) *Service {
	return seedMembers(t, map[uint]models.WorkspaceRole{
		uOwner:  models.WorkspaceRoleOwner,
		uOwner2: models.WorkspaceRoleOwner,
		uAdmin:  models.WorkspaceRoleAdmin,
		uDev:    models.WorkspaceRoleDeveloper,
	})
}

// The reported bug: an admin demoting an owner.
func TestAdminCannotDemoteOwner(t *testing.T) {
	s := fullWorkspace(t)
	err := s.UpdateMemberRole(1, models.WorkspaceRoleAdmin, uOwner, models.WorkspaceRoleViewer)
	if !errors.Is(err, ErrOutranked) {
		t.Fatalf("admin demoting an owner: got %v, want ErrOutranked", err)
	}
}

// The escalation the same endpoint allowed: an admin minting an owner.
func TestAdminCannotPromoteToOwner(t *testing.T) {
	s := fullWorkspace(t)
	err := s.UpdateMemberRole(1, models.WorkspaceRoleAdmin, uDev, models.WorkspaceRoleOwner)
	if !errors.Is(err, ErrRoleAboveSelf) {
		t.Fatalf("admin granting owner: got %v, want ErrRoleAboveSelf", err)
	}
}

// Self-promotion is the same escalation aimed inward.
func TestAdminCannotPromoteSelfToOwner(t *testing.T) {
	s := fullWorkspace(t)
	err := s.UpdateMemberRole(1, models.WorkspaceRoleAdmin, uAdmin, models.WorkspaceRoleOwner)
	if !errors.Is(err, ErrRoleAboveSelf) {
		t.Fatalf("admin self-promoting: got %v, want ErrRoleAboveSelf", err)
	}
}

func TestAdminCannotRemoveOwner(t *testing.T) {
	s := fullWorkspace(t)
	err := s.RemoveMember(1, models.WorkspaceRoleAdmin, uOwner)
	if !errors.Is(err, ErrOutranked) {
		t.Fatalf("admin removing an owner: got %v, want ErrOutranked", err)
	}
}

// An invitation is a deferred role grant and is bounded the same way.
func TestAdminCannotInviteOwner(t *testing.T) {
	s := fullWorkspace(t)
	_, _, err := s.Invite(1, models.WorkspaceRoleAdmin, uAdmin, "new@example.com", models.WorkspaceRoleOwner)
	if !errors.Is(err, ErrRoleAboveSelf) {
		t.Fatalf("admin inviting an owner: got %v, want ErrRoleAboveSelf", err)
	}
}

// Legitimate operations must keep working.
func TestOwnerCanDemoteOwner(t *testing.T) {
	s := fullWorkspace(t)
	if err := s.UpdateMemberRole(1, models.WorkspaceRoleOwner, uOwner, models.WorkspaceRoleAdmin); err != nil {
		t.Fatalf("owner demoting a co-owner: got %v, want nil", err)
	}
}

func TestAdminCanManageEqualAndLowerRanks(t *testing.T) {
	s := fullWorkspace(t)
	if err := s.UpdateMemberRole(1, models.WorkspaceRoleAdmin, uDev, models.WorkspaceRoleAdmin); err != nil {
		t.Fatalf("admin promoting a developer to admin: got %v, want nil", err)
	}
	if err := s.RemoveMember(1, models.WorkspaceRoleAdmin, uDev); err != nil {
		t.Fatalf("admin removing an equal-rank member: got %v, want nil", err)
	}
}

// The last-owner invariant still holds when the actor is entitled to act.
func TestLastOwnerStillGuarded(t *testing.T) {
	s := seedMembers(t, map[uint]models.WorkspaceRole{uOwner: models.WorkspaceRoleOwner})
	err := s.UpdateMemberRole(1, models.WorkspaceRoleOwner, uOwner, models.WorkspaceRoleAdmin)
	if !errors.Is(err, ErrLastOwner) {
		t.Fatalf("demoting the last owner: got %v, want ErrLastOwner", err)
	}
}

// An unknown/empty actor role (rank 0) must never be treated as privileged.
func TestUnknownActorRoleIsRejected(t *testing.T) {
	s := fullWorkspace(t)
	if err := s.UpdateMemberRole(1, models.WorkspaceRole(""), uDev, models.WorkspaceRoleViewer); !errors.Is(err, ErrOutranked) {
		t.Fatalf("empty actor role: got %v, want ErrOutranked", err)
	}
}

// H5: a custom role's base rank is a role grant and is bounded like one.
func TestAdminCannotAssignOwnerRankCustomRole(t *testing.T) {
	s := fullWorkspace(t)
	if err := s.AssignCustomRole(1, models.WorkspaceRoleAdmin, uAdmin, 7, models.WorkspaceRoleOwner); !errors.Is(err, ErrRoleAboveSelf) {
		t.Fatalf("admin self-assigning an owner-rank custom role: got %v, want ErrRoleAboveSelf", err)
	}
	if err := s.AssignCustomRole(1, models.WorkspaceRoleAdmin, uDev, 7, models.WorkspaceRoleOwner); !errors.Is(err, ErrRoleAboveSelf) {
		t.Fatalf("admin granting an owner-rank custom role: got %v, want ErrRoleAboveSelf", err)
	}
}

func TestAdminCannotDemoteOwnerViaCustomRole(t *testing.T) {
	s := fullWorkspace(t)
	if err := s.AssignCustomRole(1, models.WorkspaceRoleAdmin, uOwner, 7, models.WorkspaceRoleViewer); !errors.Is(err, ErrOutranked) {
		t.Fatalf("admin moving an owner onto a custom role: got %v, want ErrOutranked", err)
	}
}

func TestCustomRoleAssignmentWithinRank(t *testing.T) {
	s := fullWorkspace(t)
	if err := s.AssignCustomRole(1, models.WorkspaceRoleAdmin, uDev, 7, models.WorkspaceRoleAdmin); err != nil {
		t.Fatalf("admin assigning an admin-rank custom role: got %v, want nil", err)
	}
	if err := s.AssignCustomRole(1, models.WorkspaceRoleOwner, uOwner2, 8, models.WorkspaceRoleOwner); err != nil {
		t.Fatalf("owner moving a co-owner onto an owner-rank custom role: got %v, want nil", err)
	}
}

// Promotion to owner rank through a custom role counts against the ownership limit (0 in this fixture).
func TestOwnerRankCustomRoleRespectsOwnershipLimit(t *testing.T) {
	s := fullWorkspace(t)
	if err := s.AssignCustomRole(1, models.WorkspaceRoleOwner, uAdmin, 8, models.WorkspaceRoleOwner); !errors.Is(err, ErrWorkspaceLimitReached) {
		t.Fatalf("owner promoting an admin past the ownership limit: got %v, want ErrWorkspaceLimitReached", err)
	}
}

func TestLastOwnerGuardedOnCustomRole(t *testing.T) {
	s := seedMembers(t, map[uint]models.WorkspaceRole{uOwner: models.WorkspaceRoleOwner})
	if err := s.AssignCustomRole(1, models.WorkspaceRoleOwner, uOwner, 7, models.WorkspaceRoleAdmin); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("moving the last owner onto an admin-rank custom role: got %v, want ErrLastOwner", err)
	}
}
