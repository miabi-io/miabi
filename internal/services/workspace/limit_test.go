// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"fmt"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// seedN gives every seed() call a distinct in-memory database (seed is called
// more than once per test, so a per-test name would leak state between calls).
var seedN int

// The production models carry Postgres-specific defaults (uid uuid DEFAULT gen_random_uuid()) that sqlite
// can't migrate, so the test builds a minimal, sqlite-friendly schema for the tables the limit logic reads
// and lets the real repositories query it (see account_test.go for the same approach).

type wsRow struct {
	ID     uint `gorm:"primaryKey"`
	System bool
}

func (wsRow) TableName() string { return "workspaces" }

type memberRow struct {
	ID          uint `gorm:"primaryKey"`
	WorkspaceID uint
	UserID      uint
	Role        string
}

func (memberRow) TableName() string { return "workspace_members" }

type userRow struct {
	ID                       uint `gorm:"primaryKey"`
	WorkspaceLimit           *int
	WorkspaceMembershipLimit *int
}

func (userRow) TableName() string { return "users" }

// seed builds a service over an in-memory DB where user 1 owns `owned`
// non-system workspaces (plus one system workspace, which must never count) and
// carries the given per-user override. entitled toggles the Enterprise gate.
func seed(t *testing.T, owned int, override *int, global int, entitled bool) *Service {
	t.Helper()
	seedN++
	dsn := fmt.Sprintf("file:wslimit%d?mode=memory&cache=shared", seedN)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&wsRow{}, &memberRow{}, &userRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.Create(&userRow{ID: 1, WorkspaceLimit: override})
	// A system workspace the user owns — excluded from the count.
	db.Create(&wsRow{ID: 100, System: true})
	db.Create(&memberRow{WorkspaceID: 100, UserID: 1, Role: string(models.WorkspaceRoleOwner)})
	for i := 0; i < owned; i++ {
		ws := &wsRow{System: false}
		db.Create(ws)
		db.Create(&memberRow{WorkspaceID: ws.ID, UserID: 1, Role: string(models.WorkspaceRoleOwner)})
	}
	// A workspace the user is only a developer on — must never count as owned.
	db.Create(&wsRow{ID: 200})
	db.Create(&memberRow{WorkspaceID: 200, UserID: 1, Role: string(models.WorkspaceRoleDeveloper)})

	s := NewService(repositories.NewWorkspaceRepository(db), repositories.NewUserRepository(db), nil)
	s.SetLimits(func() int { return global }, func() bool { return entitled })
	return s
}

func TestGlobalLimitEnforced(t *testing.T) {
	// Global cap of 2, no override: at 2 owned → blocked; at 1 → allowed.
	if err := seed(t, 2, nil, 2, false).canOwnAnother(1); err != ErrWorkspaceLimitReached {
		t.Errorf("at limit: got %v, want ErrWorkspaceLimitReached", err)
	}
	if err := seed(t, 1, nil, 2, false).canOwnAnother(1); err != nil {
		t.Errorf("under limit: got %v, want nil", err)
	}
}

func TestGlobalZeroIsUnlimited(t *testing.T) {
	// Legacy convention: global 0 = unlimited.
	if err := seed(t, 9, nil, 0, false).canOwnAnother(1); err != nil {
		t.Errorf("global 0 (unlimited): got %v, want nil", err)
	}
}

func TestSystemAndNonOwnerRowsDoNotCount(t *testing.T) {
	// The user owns 1 real workspace (+ a system one they own, + a dev membership);
	// only the 1 real owned workspace counts, so a cap of 2 still allows one more.
	if err := seed(t, 1, nil, 2, false).canOwnAnother(1); err != nil {
		t.Errorf("got %v, want nil (system + non-owner rows excluded)", err)
	}
}

func TestOverrideSupersedesGlobalWhenEntitled(t *testing.T) {
	// Override of 1 beats a looser global of 5: at 1 owned → blocked.
	one := 1
	if err := seed(t, 1, &one, 5, true).canOwnAnother(1); err != ErrWorkspaceLimitReached {
		t.Errorf("override tighter than global: got %v, want ErrWorkspaceLimitReached", err)
	}
	// Override of -1 (unlimited) beats a strict global of 1.
	unlimited := -1
	if err := seed(t, 9, &unlimited, 1, true).canOwnAnother(1); err != nil {
		t.Errorf("override unlimited over global cap: got %v, want nil", err)
	}
}

func TestOverrideIgnoredWithoutEntitlement(t *testing.T) {
	// A generous override is ignored when the license isn't entitled; the strict
	// global (1) applies, so an already-owning user is blocked.
	ten := 10
	if err := seed(t, 1, &ten, 1, false).canOwnAnother(1); err != ErrWorkspaceLimitReached {
		t.Errorf("override without entitlement: got %v, want ErrWorkspaceLimitReached (global applies)", err)
	}
}

func TestUnwiredLimitsAllowEverything(t *testing.T) {
	// A service with no limits wired never blocks.
	s := seed(t, 9, nil, 3, false)
	s.SetLimits(nil, nil)
	if err := s.canOwnAnother(1); err != nil {
		t.Errorf("unwired: got %v, want nil", err)
	}
}

// --- membership (workspaces-joined) limit ---

// seedJoined builds a service where user 1 is a non-owner member of `joined`
// non-system workspaces — plus one workspace they OWN and the system workspace,
// neither of which may count toward the membership limit.
func seedJoined(t *testing.T, joined int, override *int, global int, entitled bool) *Service {
	t.Helper()
	seedN++
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:msjoin%d?mode=memory&cache=shared", seedN)), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&wsRow{}, &memberRow{}, &userRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.Create(&userRow{ID: 1, WorkspaceMembershipLimit: override})
	// A system workspace the user is a member of — excluded.
	db.Create(&wsRow{ID: 100, System: true})
	db.Create(&memberRow{WorkspaceID: 100, UserID: 1, Role: string(models.WorkspaceRoleViewer)})
	// A workspace the user OWNS — counts as owned, never as joined.
	db.Create(&wsRow{ID: 200})
	db.Create(&memberRow{WorkspaceID: 200, UserID: 1, Role: string(models.WorkspaceRoleOwner)})
	for i := 0; i < joined; i++ {
		ws := &wsRow{System: false}
		db.Create(ws)
		db.Create(&memberRow{WorkspaceID: ws.ID, UserID: 1, Role: string(models.WorkspaceRoleDeveloper)})
	}
	s := NewService(repositories.NewWorkspaceRepository(db), repositories.NewUserRepository(db), nil)
	s.SetMembershipLimits(func() int { return global }, func() bool { return entitled })
	return s
}

func TestMembershipGlobalLimitEnforced(t *testing.T) {
	if err := seedJoined(t, 2, nil, 2, false).CanJoinAnother(1); err != ErrMembershipLimitReached {
		t.Errorf("at limit: got %v, want ErrMembershipLimitReached", err)
	}
	if err := seedJoined(t, 1, nil, 2, false).CanJoinAnother(1); err != nil {
		t.Errorf("under limit: got %v, want nil", err)
	}
}

func TestMembershipOwnedAndSystemDoNotCount(t *testing.T) {
	// User owns 1 workspace and is a member of the system one, but has joined only
	// 1 real workspace, so a cap of 2 still allows one more.
	if err := seedJoined(t, 1, nil, 2, false).CanJoinAnother(1); err != nil {
		t.Errorf("got %v, want nil (owned + system excluded)", err)
	}
}

func TestMembershipOverrideSupersedesGlobalWhenEntitled(t *testing.T) {
	one := 1
	if err := seedJoined(t, 1, &one, 5, true).CanJoinAnother(1); err != ErrMembershipLimitReached {
		t.Errorf("override tighter than global: got %v, want ErrMembershipLimitReached", err)
	}
	unlimited := -1
	if err := seedJoined(t, 9, &unlimited, 1, true).CanJoinAnother(1); err != nil {
		t.Errorf("override unlimited over global cap: got %v, want nil", err)
	}
}

func TestMembershipOverrideIgnoredWithoutEntitlement(t *testing.T) {
	ten := 10
	if err := seedJoined(t, 1, &ten, 1, false).CanJoinAnother(1); err != ErrMembershipLimitReached {
		t.Errorf("override without entitlement: got %v, want ErrMembershipLimitReached (global applies)", err)
	}
}
