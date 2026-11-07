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

package repositories

import (
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Stand-in for organizations: the real uid column defaults to Postgres-only gen_random_uuid().
type ownerOrgTable struct {
	ID          uint `gorm:"primaryKey"`
	Name        string
	OwnerUserID uint
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (ownerOrgTable) TableName() string { return "organizations" }

func newUserOwnerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.User{}, &ownerOrgTable{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func ownerRow(t *testing.T, db *gorm.DB, id uint) uint {
	t.Helper()
	var org ownerOrgTable
	if err := db.First(&org, id).Error; err != nil {
		t.Fatal(err)
	}
	return org.OwnerUserID
}

// Deleting the owner must release its organizations, or nothing could administer them.
func TestUserRepositoryDelete_ReleasesOwnedOrganizations(t *testing.T) {
	db := newUserOwnerDB(t)
	repo := NewUserRepository(db)

	owner := &models.User{Name: "Owner", Email: "owner@acme.test", PasswordHash: "x", Active: true}
	keeper := &models.User{Name: "Keeper", Email: "keeper@globex.test", PasswordHash: "x", Active: true}
	for _, u := range []*models.User{owner, keeper} {
		if err := db.Create(u).Error; err != nil {
			t.Fatal(err)
		}
	}
	orgs := []ownerOrgTable{
		{ID: 1, Name: "acme", OwnerUserID: owner.ID},
		{ID: 2, Name: "acme-two", OwnerUserID: owner.ID},
		{ID: 3, Name: "globex", OwnerUserID: keeper.ID},
	}
	for i := range orgs {
		if err := db.Create(&orgs[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := repo.Delete(owner.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, id := range []uint{1, 2} {
		if got := ownerRow(t, db, id); got != 0 {
			t.Errorf("organization %d owner = %d, want released", id, got)
		}
	}
	if got := ownerRow(t, db, 3); got != keeper.ID {
		t.Errorf("another user's organization = %d, want it untouched at %d", got, keeper.ID)
	}
	if err := db.First(&models.User{}, owner.ID).Error; err == nil {
		t.Error("the account itself should be gone")
	}
}

// Deleting a user who owns nothing is the ordinary case and must not be disturbed by the release.
func TestUserRepositoryDelete_OwnsNothing(t *testing.T) {
	db := newUserOwnerDB(t)
	repo := NewUserRepository(db)

	u := &models.User{Name: "Plain", Email: "plain@acme.test", PasswordHash: "x", Active: true}
	if err := db.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(u.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := db.First(&models.User{}, u.ID).Error; err == nil {
		t.Error("the account should be gone")
	}
}
