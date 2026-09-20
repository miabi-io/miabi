// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func ownerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&User{}); err != nil {
		t.Fatal(err)
	}
	// Organization's uid column defaults to the Postgres-only gen_random_uuid(), which sqlite
	// cannot parse, so the table the hooks write to is created by hand.
	err = db.Exec(`CREATE TABLE organizations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT, display_name TEXT,
		is_default NUMERIC NOT NULL DEFAULT 0,
		owner_user_id INTEGER NOT NULL DEFAULT 0,
		max_workspaces INTEGER NOT NULL DEFAULT -1,
		created_at datetime, updated_at datetime)`).Error
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func newOrg(t *testing.T, db *gorm.DB, name string, owner uint) *Organization {
	t.Helper()
	err := db.Exec(`INSERT INTO organizations (name, display_name, owner_user_id) VALUES (?, ?, ?)`,
		name, name, owner).Error
	if err != nil {
		t.Fatal(err)
	}
	var org Organization
	if err := db.Table("organizations").Where("name = ?", name).Take(&org).Error; err != nil {
		t.Fatal(err)
	}
	return &org
}

func newUser(t *testing.T, db *gorm.DB, email string, org *uint) *User {
	t.Helper()
	u := &User{Name: email, Email: email, PasswordHash: "x", Role: SystemRoleUser, Active: true, OrganizationID: org}
	if err := db.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	return u
}

func ownerOf(t *testing.T, db *gorm.DB, orgID uint) uint {
	t.Helper()
	var org Organization
	if err := db.Table("organizations").Where("id = ?", orgID).Take(&org).Error; err != nil {
		t.Fatal(err)
	}
	return org.OwnerUserID
}

// An organization provisioned by SSO would otherwise be accountable to nobody, so its first user
// adopts it.
func TestUserAfterCreate_FirstUserAdoptsTheOrganization(t *testing.T) {
	db := ownerDB(t)
	org := newOrg(t, db, "acme", 0)

	first := newUser(t, db, "first@acme.test", &org.ID)
	if got := ownerOf(t, db, org.ID); got != first.ID {
		t.Fatalf("owner = %d, want the first user %d", got, first.ID)
	}

	second := newUser(t, db, "second@acme.test", &org.ID)
	if got := ownerOf(t, db, org.ID); got != first.ID {
		t.Fatalf("owner = %d after a second user, want it unchanged at %d", got, second.ID)
	}
}

// An owner an admin named is a decision, not a gap to fill: the next user must not take it.
func TestUserAfterCreate_NeverOverridesAChosenOwner(t *testing.T) {
	db := ownerDB(t)
	org := newOrg(t, db, "acme", 0)
	chosen := newUser(t, db, "chosen@acme.test", nil)
	if err := db.Model(&Organization{}).Where("id = ?", org.ID).Update("owner_user_id", chosen.ID).Error; err != nil {
		t.Fatal(err)
	}

	newUser(t, db, "later@acme.test", &org.ID)
	if got := ownerOf(t, db, org.ID); got != chosen.ID {
		t.Fatalf("owner = %d, want the chosen owner %d", got, chosen.ID)
	}
}

// A user in no organization claims nothing: the default organization's owner is the first platform
// admin, assigned at seeding, not whoever signs up first.
func TestUserAfterCreate_NoOrganizationClaimsNothing(t *testing.T) {
	db := ownerDB(t)
	org := newOrg(t, db, "acme", 0)

	newUser(t, db, "nobody@acme.test", nil)
	if got := ownerOf(t, db, org.ID); got != 0 {
		t.Fatalf("owner = %d, want none", got)
	}
}
