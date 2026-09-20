// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ownerOrgTable stands in for organizations: the real model's uid column defaults to the
// Postgres-only gen_random_uuid(), which sqlite cannot parse.
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

// An organization left accountable to a deleted account cannot be administered: nothing resolves
// its owner, and nothing hands it to anyone else. Deleting the owner releases it, and only it.
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
