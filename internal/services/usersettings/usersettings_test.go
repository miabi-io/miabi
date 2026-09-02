// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package usersettings

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

type fakeUsers struct {
	defaultWS map[uint]*uint
	ws        *models.WorkspaceWithRole
}

func (f *fakeUsers) DefaultWorkspace(userID uint) (*models.WorkspaceWithRole, error) {
	if f.defaultWS[userID] == nil {
		return nil, nil
	}
	return f.ws, nil
}

func (f *fakeUsers) SetDefaultWorkspace(userID uint, workspaceID *uint) error {
	if f.defaultWS == nil {
		f.defaultWS = map[uint]*uint{}
	}
	f.defaultWS[userID] = workspaceID
	return nil
}

func (f *fakeUsers) FindByID(id uint) (*models.User, error) {
	return &models.User{ID: id, DefaultWorkspaceID: f.defaultWS[id]}, nil
}

type fakeSettings struct{ rows map[uint]*models.UserSetting }

func (f *fakeSettings) Get(userID uint) (*models.UserSetting, error) {
	if s, ok := f.rows[userID]; ok {
		return s, nil
	}
	d := models.DefaultUserSetting()
	d.UserID = userID
	return &d, nil
}

func (f *fakeSettings) Save(s *models.UserSetting) error {
	if f.rows == nil {
		f.rows = map[uint]*models.UserSetting{}
	}
	f.rows[s.UserID] = s
	return nil
}

type fakeMembers struct{ member map[[2]uint]bool }

func (f fakeMembers) FindMember(workspaceID, userID uint) (*models.WorkspaceMember, error) {
	if f.member[[2]uint{workspaceID, userID}] {
		return &models.WorkspaceMember{WorkspaceID: workspaceID, UserID: userID}, nil
	}
	return nil, errors.New("record not found")
}

func TestSetDefaultWorkspaceRequiresMembership(t *testing.T) {
	users := &fakeUsers{}
	s := NewService(users, &fakeSettings{}, fakeMembers{member: map[[2]uint]bool{{7, 1}: true}})

	other := uint(9)
	if err := s.SetDefaultWorkspace(1, &other); !errors.Is(err, ErrNotMember) {
		t.Fatalf("defaulting to a non-member workspace = %v, want ErrNotMember", err)
	}
	if users.defaultWS[1] != nil {
		t.Fatal("a refused default must not be written")
	}
	mine := uint(7)
	if err := s.SetDefaultWorkspace(1, &mine); err != nil {
		t.Fatalf("defaulting to a workspace the user belongs to = %v, want nil", err)
	}
	// A nil id clears the choice, and must not consult membership at all — there is
	// no workspace to be a member of.
	if err := s.SetDefaultWorkspace(1, nil); err != nil {
		t.Fatalf("clearing the default = %v, want nil", err)
	}
	if users.defaultWS[1] != nil {
		t.Fatal("clearing must store nil so the next read falls back to the oldest membership")
	}
}

func TestAdoptFirstWorkspaceOnlyClaimsOnce(t *testing.T) {
	users := &fakeUsers{}
	s := NewService(users, &fakeSettings{}, fakeMembers{})

	s.AdoptFirstWorkspace(1, 42)
	if users.defaultWS[1] == nil || *users.defaultWS[1] != 42 {
		t.Fatalf("first workspace = %v, want it adopted as the default", users.defaultWS[1])
	}
	s.AdoptFirstWorkspace(1, 99)
	if *users.defaultWS[1] != 42 {
		t.Fatal("a later workspace must not overwrite an existing default")
	}
}

func TestSaveValidatesEnums(t *testing.T) {
	s := NewService(&fakeUsers{}, &fakeSettings{}, fakeMembers{})

	bad := "chartreuse"
	if _, err := s.Save(1, Update{Theme: &bad}); !errors.Is(err, ErrInvalidTheme) {
		t.Fatalf("theme %q = %v, want ErrInvalidTheme", bad, err)
	}
	view := "../../etc/passwd"
	if _, err := s.Save(1, Update{LandingView: &view}); !errors.Is(err, ErrInvalidLandingView) {
		t.Fatalf("landing view %q = %v, want ErrInvalidLandingView", view, err)
	}
}

func TestSavePartialUpdate(t *testing.T) {
	s := NewService(&fakeUsers{}, &fakeSettings{}, fakeMembers{})

	tz := "Europe/Berlin"
	if _, err := s.Save(1, Update{Timezone: &tz}); err != nil {
		t.Fatalf("save timezone: %v", err)
	}
	dark := "dark"
	out, err := s.Save(1, Update{Theme: &dark})
	if err != nil {
		t.Fatalf("save theme: %v", err)
	}
	if out.Timezone != tz {
		t.Fatalf("timezone = %q after a theme-only update, want it preserved", out.Timezone)
	}
	if out.Theme != models.ThemeDark {
		t.Fatalf("theme = %q, want dark", out.Theme)
	}
	if out.LandingView != "dashboard" {
		t.Fatalf("landing view = %q, want the default preserved", out.LandingView)
	}
}

func TestValidThemeAndLandingView(t *testing.T) {
	for _, ok := range []models.Theme{models.ThemeSystem, models.ThemeLight, models.ThemeDark} {
		if !models.ValidTheme(ok) {
			t.Fatalf("%q must be a valid theme", ok)
		}
	}
	if models.ValidTheme("") || models.ValidTheme("solarized") {
		t.Fatal("unknown themes must be refused")
	}
	if !models.ValidLandingView("dashboard") || !models.ValidLandingView("apps") {
		t.Fatal("known console sections must be accepted")
	}
	if models.ValidLandingView("") || models.ValidLandingView("admin") {
		t.Fatal("unknown or non-workspace sections must be refused")
	}
}

func TestDefaultUserSettingIsUsable(t *testing.T) {
	d := models.DefaultUserSetting()
	if !models.ValidTheme(d.Theme) || !models.ValidLandingView(d.LandingView) {
		t.Fatalf("defaults are not self-consistent: %+v", d)
	}
	if d.Timezone == "" || d.Locale == "" {
		t.Fatalf("defaults leave display fields empty: %+v", d)
	}
}
