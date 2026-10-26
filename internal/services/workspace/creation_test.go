// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

type creationWsRow struct {
	ID             uint `gorm:"primaryKey"`
	System         bool
	DisplayName    string
	OrganizationID *uint
}

func (creationWsRow) TableName() string { return "workspaces" }

type creationMemberRow struct {
	ID           uint `gorm:"primaryKey"`
	WorkspaceID  uint
	UserID       uint
	Role         string
	CustomRoleID *uint
	ManagedBy    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (creationMemberRow) TableName() string { return "workspace_members" }

type creationOrgRow struct {
	ID          uint `gorm:"primaryKey"`
	OwnerUserID uint
	UpdatedAt   time.Time
}

func (creationOrgRow) TableName() string { return "organizations" }

func creationDB(t *testing.T) *gorm.DB {
	t.Helper()
	seedN++
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:wscreation%d?mode=memory&cache=shared", seedN)), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&creationWsRow{}, &creationMemberRow{}, &creationOrgRow{}, &models.User{}, &models.WorkspaceInvitation{}); err != nil {
		t.Fatal(err)
	}
	return db
}

type creationCase struct {
	platform  string
	orgMode   *string
	orgOwner  uint
	override  *bool
	entitled  bool
	role      models.SystemRole
	source    string
	wantAllow bool
	wantSrc   string
}

func (c creationCase) run(t *testing.T) CreationPolicy {
	t.Helper()
	db := creationDB(t)
	u := &models.User{Email: "u@example.com", Role: c.role, Active: true, SignupSource: c.source, CanCreateWorkspaces: c.override}
	if u.Role == "" {
		u.Role = models.SystemRoleUser
	}
	if err := db.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	s := NewService(repositories.NewWorkspaceRepository(db), repositories.NewUserRepository(db), nil)
	s.SetLimits(func() int { return -1 }, func() bool { return c.entitled })
	s.SetCreationPolicy(func() string { return c.platform })
	owner := c.orgOwner
	if owner == 1 {
		owner = u.ID
	}
	s.SetOrgs(fakeOrgs{org: &models.Organization{ID: 7, OwnerUserID: owner, WorkspaceCreation: c.orgMode}})
	s.SetOrgLimitsEntitled(func() bool { return c.entitled })
	return s.CreationPolicy(u.ID)
}

func TestCreationPolicyResolution(t *testing.T) {
	adminsOnly := models.WorkspaceCreationAdminsOnly
	anyone := models.WorkspaceCreationAnyone
	yes, no := true, false
	cases := map[string]creationCase{
		"anyone lets an invitee create": {
			platform: anyone, source: models.SignupSourceInvitation, wantAllow: true, wantSrc: CreationSourcePlatform,
		},
		"except_invited refuses an invitee": {
			platform: models.WorkspaceCreationExceptInvited, source: models.SignupSourceInvitation, wantSrc: CreationSourcePlatform,
		},
		"except_invited lets a self sign-up create": {
			platform: models.WorkspaceCreationExceptInvited, source: models.SignupSourceSelf, wantAllow: true, wantSrc: CreationSourcePlatform,
		},
		"except_invited lets an account that predates signup sources create": {
			platform: models.WorkspaceCreationExceptInvited, wantAllow: true, wantSrc: CreationSourcePlatform,
		},
		"admins_only refuses a regular user": {
			platform: adminsOnly, source: models.SignupSourceSelf, wantSrc: CreationSourcePlatform,
		},
		"admins_only lets a platform admin create": {
			platform: adminsOnly, role: models.SystemRoleAdmin, wantAllow: true, wantSrc: CreationSourceAdmin,
		},
		"admins_only lets the organization owner create": {
			platform: adminsOnly, orgOwner: 1, wantAllow: true, wantSrc: CreationSourcePlatform,
		},
		"licensed organization mode beats the platform": {
			platform: anyone, orgMode: &adminsOnly, entitled: true, wantSrc: CreationSourceOrganization,
		},
		"unlicensed organization mode is ignored": {
			platform: anyone, orgMode: &adminsOnly, wantAllow: true, wantSrc: CreationSourcePlatform,
		},
		"licensed user override allows past the organization": {
			platform: adminsOnly, orgMode: &adminsOnly, override: &yes, entitled: true, wantAllow: true, wantSrc: CreationSourceUser,
		},
		"licensed user override denies under anyone": {
			platform: anyone, override: &no, entitled: true, wantSrc: CreationSourceUser,
		},
		"unlicensed user override is ignored": {
			platform: anyone, override: &no, wantAllow: true, wantSrc: CreationSourcePlatform,
		},
		"console refuses a platform admin": {
			platform: models.WorkspaceCreationConsole, role: models.SystemRoleAdmin, wantSrc: CreationSourcePlatform,
		},
		"console outranks a licensed user override": {
			platform: models.WorkspaceCreationConsole, override: &yes, entitled: true, wantSrc: CreationSourcePlatform,
		},
		"an unknown platform mode falls back to anyone": {
			platform: "bogus", source: models.SignupSourceInvitation, wantAllow: true, wantSrc: CreationSourcePlatform,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := c.run(t)
			if got.Allowed != c.wantAllow || got.Source != c.wantSrc {
				t.Errorf("got %+v, want allowed=%v source=%s", got, c.wantAllow, c.wantSrc)
			}
		})
	}
}

func TestCreateRefusedByPolicyCarriesTheErrorCode(t *testing.T) {
	db := creationDB(t)
	u := &models.User{Email: "i@example.com", Role: models.SystemRoleUser, Active: true, SignupSource: models.SignupSourceInvitation}
	db.Create(u)
	s := NewService(repositories.NewWorkspaceRepository(db), repositories.NewUserRepository(db), nil)
	s.SetCreationPolicy(func() string { return models.WorkspaceCreationExceptInvited })

	_, err := s.Create(u.ID, "Mine", "", "")
	if !errors.Is(err, ErrWorkspaceCreationNotAllowed) {
		t.Fatalf("got %v, want ErrWorkspaceCreationNotAllowed", err)
	}
	var coded interface{ Code() string }
	if !errors.As(err, &coded) || coded.Code() != "WORKSPACE_CREATION_NOT_ALLOWED" {
		t.Errorf("error code: got %v", err)
	}
}

func TestConsoleModeErrorCode(t *testing.T) {
	db := creationDB(t)
	u := &models.User{Email: "a@example.com", Role: models.SystemRoleAdmin, Active: true}
	db.Create(u)
	s := NewService(repositories.NewWorkspaceRepository(db), repositories.NewUserRepository(db), nil)
	s.SetCreationPolicy(func() string { return models.WorkspaceCreationConsole })

	if !s.ConsoleOnly() {
		t.Fatal("ConsoleOnly() = false under console mode")
	}
	_, err := s.Create(u.ID, "Mine", "", "")
	var coded interface{ Code() string }
	if !errors.As(err, &coded) || coded.Code() != "WORKSPACE_CREATION_CONSOLE" {
		t.Errorf("error = %v, want WORKSPACE_CREATION_CONSOLE", err)
	}
}

func seedInvitation(t *testing.T, db *gorm.DB, email string, expires time.Time) (string, *models.WorkspaceInvitation) {
	t.Helper()
	org := uint(7)
	db.Create(&creationWsRow{ID: 1, DisplayName: "Acme", OrganizationID: &org})
	raw, hash := generateToken()
	inv := &models.WorkspaceInvitation{
		WorkspaceID: 1, Email: email, Role: models.WorkspaceRoleDeveloper, TokenHash: hash,
		Status: models.InvitationStatusPending, InvitedBy: 99, ExpiresAt: expires,
	}
	if err := db.Create(inv).Error; err != nil {
		t.Fatal(err)
	}
	return raw, inv
}

func TestRegisterFromInvitationCreatesAndJoins(t *testing.T) {
	db := creationDB(t)
	raw, inv := seedInvitation(t, db, "new@example.com", time.Now().Add(time.Hour))
	s := NewService(repositories.NewWorkspaceRepository(db), repositories.NewUserRepository(db), nil)

	u := &models.User{Name: "New", Email: "attacker@example.com", PasswordHash: "x", Role: models.SystemRoleUser, Active: true}
	ws, err := s.RegisterFromInvitation(raw, u)
	if err != nil {
		t.Fatal(err)
	}
	if ws.ID != 1 {
		t.Errorf("joined workspace %d, want 1", ws.ID)
	}
	if u.Email != "new@example.com" || u.SignupSource != models.SignupSourceInvitation {
		t.Errorf("user email/source = %s/%s, want the invited email and invitation", u.Email, u.SignupSource)
	}
	if u.OrganizationID == nil || *u.OrganizationID != 7 {
		t.Errorf("organization = %v, want the workspace's (7)", u.OrganizationID)
	}
	var m creationMemberRow
	if err := db.Where("workspace_id = 1 AND user_id = ?", u.ID).First(&m).Error; err != nil || m.Role != string(models.WorkspaceRoleDeveloper) {
		t.Errorf("membership: %+v, %v", m, err)
	}
	var got models.WorkspaceInvitation
	db.First(&got, inv.ID)
	if got.Status != models.InvitationStatusAccepted {
		t.Errorf("invitation status %s, want accepted", got.Status)
	}

	if _, err := s.RegisterFromInvitation(raw, &models.User{Name: "Again", PasswordHash: "x"}); !errors.Is(err, ErrInvalidInvite) {
		t.Errorf("reusing the token: got %v, want ErrInvalidInvite", err)
	}
}

func TestRegisterFromInvitationRefusals(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		db := creationDB(t)
		raw, _ := seedInvitation(t, db, "late@example.com", time.Now().Add(-time.Minute))
		s := NewService(repositories.NewWorkspaceRepository(db), repositories.NewUserRepository(db), nil)
		if _, err := s.RegisterFromInvitation(raw, &models.User{Name: "L", PasswordHash: "x"}); !errors.Is(err, ErrInvalidInvite) {
			t.Errorf("got %v, want ErrInvalidInvite", err)
		}
	})
	t.Run("account already exists", func(t *testing.T) {
		db := creationDB(t)
		raw, _ := seedInvitation(t, db, "taken@example.com", time.Now().Add(time.Hour))
		db.Create(&models.User{Email: "taken@example.com", Role: models.SystemRoleUser, Active: true})
		s := NewService(repositories.NewWorkspaceRepository(db), repositories.NewUserRepository(db), nil)
		if _, err := s.RegisterFromInvitation(raw, &models.User{Name: "T", PasswordHash: "x"}); !errors.Is(err, ErrInvalidInvite) {
			t.Errorf("got %v, want ErrInvalidInvite", err)
		}
		var n int64
		db.Model(&models.User{}).Count(&n)
		if n != 1 {
			t.Errorf("users = %d, want 1", n)
		}
	})
}

func TestFailedJoinLeavesNoAccount(t *testing.T) {
	db := creationDB(t)
	_, inv := seedInvitation(t, db, "race@example.com", time.Now().Add(time.Hour))
	db.Model(inv).Update("status", models.InvitationStatusAccepted)
	repo := repositories.NewWorkspaceRepository(db)

	err := repo.CreateUserFromInvitation(&models.User{Email: inv.Email, PasswordHash: "x", Role: models.SystemRoleUser}, inv)
	if !errors.Is(err, repositories.ErrInvitationClaimed) {
		t.Fatalf("got %v, want ErrInvitationClaimed", err)
	}
	var n int64
	db.Model(&models.User{}).Count(&n)
	if n != 0 {
		t.Errorf("users = %d, want 0", n)
	}
}
