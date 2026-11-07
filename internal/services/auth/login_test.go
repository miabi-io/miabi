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

package auth

import (
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func authTestService(t *testing.T) *Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.User{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("s3cret-pass"), bcrypt.DefaultCost)
	users := []models.User{
		{Name: "Jane", Username: "jane", Email: "jane@example.com", PasswordHash: string(hash), Role: models.SystemRoleUser, Active: true},
		{Name: "Off", Username: "off", Email: "off@example.com", PasswordHash: string(hash), Role: models.SystemRoleUser, Active: false},
	}
	for i := range users {
		if err := db.Create(&users[i]).Error; err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	// GORM applies default:true to a zero-value Active at insert, so disable explicitly.
	if err := db.Model(&models.User{}).Where("username = ?", "off").Update("active", false).Error; err != nil {
		t.Fatalf("disable user: %v", err)
	}
	// Authenticate only touches the user repository.
	return NewService(repositories.NewUserRepository(db), nil, nil, nil, "test-secret")
}

func TestAuthenticateByEmailOrUsername(t *testing.T) {
	s := authTestService(t)

	cases := []struct {
		name       string
		identifier string
		password   string
		wantErr    error
	}{
		{"by email", "jane@example.com", "s3cret-pass", nil},
		{"by username", "jane", "s3cret-pass", nil},
		{"email case-insensitive", "JANE@example.com", "s3cret-pass", nil},
		{"wrong password", "jane", "nope", ErrInvalidCredentials},
		{"unknown handle", "ghost", "s3cret-pass", ErrInvalidCredentials},
		{"disabled account", "off", "s3cret-pass", ErrAccountDisabled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := s.Authenticate(tc.identifier, tc.password)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Authenticate(%q) err = %v, want %v", tc.identifier, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Authenticate(%q) unexpected error: %v", tc.identifier, err)
			}
			if u.Username != "jane" {
				t.Errorf("resolved user = %q, want jane", u.Username)
			}
		})
	}
}

func TestServiceAccountCannotSignIn(t *testing.T) {
	s := authTestService(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("s3cret-pass"), bcrypt.DefaultCost)
	ws := uint(1)
	sa := models.User{Name: "CI", Username: "sa-ci", Email: "sa-ci@" + models.ServiceAccountEmailDomain, PasswordHash: string(hash),
		Role: models.SystemRoleUser, Active: true, Kind: models.UserKindService, ServiceWorkspaceID: &ws}
	if err := s.users.Create(&sa); err != nil {
		t.Fatal(err)
	}
	// A matching password must still be refused: the kind decides, not the hash.
	if _, err := s.Authenticate("sa-ci", "s3cret-pass"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Authenticate = %v, want ErrInvalidCredentials", err)
	}
	if tok, _, err := s.IssueToken(&sa); !errors.Is(err, ErrServiceAccountSession) || tok != "" {
		t.Fatalf("IssueToken = %q, %v", tok, err)
	}
	if raw, _, err := s.CreatePasswordReset(sa.Email); raw != "" || err != nil {
		t.Fatalf("CreatePasswordReset = %q, %v", raw, err)
	}
}
