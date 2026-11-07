//go:build enterprise

// Copyright (c) 2026 Jonas Kaninda. All rights reserved.
//
// This file is part of Miabi Enterprise Edition.
//
// This file is licensed under the Miabi Enterprise License, not the GNU Affero
// General Public License. You may use it only with a valid Miabi Enterprise
// subscription and under the terms in internal/enterprise/LICENSE.md.
//
// SPDX-License-Identifier: LicenseRef-Miabi-Enterprise

package ldap

import (
	"context"
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func testProvider(t *testing.T, seed ...*models.LDAPConfig) *Provider {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&models.LDAPConfig{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, c := range seed {
		if err := db.Create(c).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	p, err := New(Deps{DB: db})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return p
}

// An empty password must never bind, guarding the anonymous-bind bypass.
func TestAuthenticate_EmptyPasswordRejected(t *testing.T) {
	p := testProvider(t, &models.LDAPConfig{
		Name: "corp", Host: "ldap.invalid", Port: 389, Enabled: true,
		UserFilter: "(uid=%s)", UserBaseDN: "dc=c",
	})
	_, err := p.Authenticate(context.Background(), "alice", "")
	if !errors.Is(err, ErrNoMatch) {
		t.Fatalf("empty password: want ErrNoMatch (no bind attempted), got %v", err)
	}
}

func TestAuthenticate_EmptyUsernameRejected(t *testing.T) {
	p := testProvider(t)
	_, err := p.Authenticate(context.Background(), "  ", "pw")
	if !errors.Is(err, ErrNoMatch) {
		t.Fatalf("empty username: want ErrNoMatch, got %v", err)
	}
}

func TestAuthenticate_NoConfigs(t *testing.T) {
	p := testProvider(t) // no LDAP configs at all
	_, err := p.Authenticate(context.Background(), "alice", "pw")
	if !errors.Is(err, ErrNoMatch) {
		t.Fatalf("no configs: want ErrNoMatch, got %v", err)
	}
}

func TestAuthenticate_NotEntitled(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	_ = db.AutoMigrate(&models.LDAPConfig{})
	_ = db.Create(&models.LDAPConfig{Name: "corp", Host: "h", Port: 389, Enabled: true}).Error
	p, err := New(Deps{DB: db, Entitled: func() bool { return false }})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, err := p.Authenticate(context.Background(), "alice", "pw"); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("unentitled: want ErrNoMatch, got %v", err)
	}
}

func TestHelpers(t *testing.T) {
	if attrOr("", "mail") != "mail" || attrOr("upn", "mail") != "upn" {
		t.Error("attrOr")
	}
	if got := groupFilter(&models.LDAPConfig{}, "cn=a,dc=c"); got != "(member=cn=a,dc=c)" {
		t.Errorf("groupFilter default = %q", got)
	}
	if got := groupFilter(&models.LDAPConfig{NestedGroups: true}, "cn=a,dc=c"); got != "(member:1.2.840.113556.1.4.1941:=cn=a,dc=c)" {
		t.Errorf("groupFilter nested = %q", got)
	}
	if got := dedupe([]string{"a", "a", "", "b"}); len(got) != 2 {
		t.Errorf("dedupe = %v", got)
	}
}
