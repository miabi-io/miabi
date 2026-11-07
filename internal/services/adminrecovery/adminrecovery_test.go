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

package adminrecovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type recordedNotices struct{ titles []string }

func (r *recordedNotices) NotifyAdmins(n models.Notification) error {
	r.titles = append(r.titles, n.Title)
	return nil
}

type fixture struct {
	svc     *Service
	db      *gorm.DB
	mr      *miniredis.Miniredis
	notices *recordedNotices
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.Session{}, &models.TwoFactorRecoveryCode{}, &models.AuditLog{}); err != nil {
		t.Fatal(err)
	}
	mr := miniredis.RunT(t)
	notices := &recordedNotices{}
	return fixture{svc: NewService(db, redis.NewClient(&redis.Options{Addr: mr.Addr()}), notices), db: db, mr: mr, notices: notices}
}

func (f fixture) lockedOutAdmin(t *testing.T) *models.User {
	t.Helper()
	u := &models.User{
		Name: "Admin", Email: "admin@acme.test", PasswordHash: "old", Role: models.SystemRoleAdmin,
		Active: true, TwoFactorEnabled: true, TwoFactorSecret: "enc-secret",
	}
	if err := f.db.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	f.db.Model(u).Update("active", false)
	f.db.Create(&models.TwoFactorRecoveryCode{UserID: u.ID, CodeHash: "h1"})
	f.db.Create(&models.Session{UserID: u.ID, JTI: "live-jti", ExpiresAt: time.Now().Add(time.Hour)})
	f.db.Create(&models.Session{UserID: u.ID, JTI: "old-jti", ExpiresAt: time.Now().Add(-time.Hour)})
	_ = f.mr.Set("admin:elev:lock:1", "123")
	_ = f.mr.Set("admin:elev:fail:1", "4")
	stored, err := f.svc.Admin("ADMIN@acme.test ")
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func TestRecoverReopensEveryWayIn(t *testing.T) {
	f := newFixture(t)
	u := f.lockedOutAdmin(t)

	res, err := f.svc.Recover(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	if !res.TwoFactorRemoved || !res.Reactivated || res.SessionsEnded != 1 || len(res.Password) != 20 {
		t.Fatalf("result = %+v", res)
	}

	var stored models.User
	f.db.First(&stored, u.ID)
	if bcrypt.CompareHashAndPassword([]byte(stored.PasswordHash), []byte(res.Password)) != nil {
		t.Error("the printed password does not sign in")
	}
	if !stored.MustChangePassword || !stored.Active || stored.TwoFactorEnabled || stored.TwoFactorSecret != "" {
		t.Errorf("stored user = must_change %v, active %v, 2fa %v, secret %q",
			stored.MustChangePassword, stored.Active, stored.TwoFactorEnabled, stored.TwoFactorSecret)
	}
	var codes, live int64
	f.db.Model(&models.TwoFactorRecoveryCode{}).Where("user_id = ?", u.ID).Count(&codes)
	f.db.Model(&models.Session{}).Where("user_id = ? AND revoked = ?", u.ID, false).Count(&live)
	if codes != 0 || live != 0 {
		t.Errorf("%d recovery codes and %d unrevoked sessions left", codes, live)
	}
	if !f.mr.Exists("session:revoked:live-jti") || f.mr.Exists("admin:elev:lock:1") || f.mr.Exists("admin:elev:fail:1") {
		t.Error("the live session was not revoked in Redis, or the unlock lockout was left")
	}
	var audit models.AuditLog
	if err := f.db.Where("action = ?", "admin.break_glass").First(&audit).Error; err != nil {
		t.Errorf("no break-glass audit entry: %v", err)
	}
	if len(f.notices.titles) != 1 {
		t.Errorf("admins were told %d times, want once", len(f.notices.titles))
	}
}

func TestOnlyPlatformAdminsAreRecovered(t *testing.T) {
	f := newFixture(t)
	f.db.Create(&models.User{Name: "Dev", Email: "dev@acme.test", PasswordHash: "x", Role: models.SystemRoleUser, Active: true})

	if _, err := f.svc.Admin("dev@acme.test"); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("err = %v, want ErrNotAdmin", err)
	}
	if _, err := f.svc.Admin("nobody@acme.test"); !errors.Is(err, ErrNoUser) {
		t.Fatalf("err = %v, want ErrNoUser", err)
	}
}

func TestResetPasswordWithAChosenPassword(t *testing.T) {
	f := newFixture(t)
	u := f.lockedOutAdmin(t)

	if _, err := f.svc.ResetPassword(context.Background(), u, "short"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("err = %v, want ErrWeakPassword", err)
	}
	res, err := f.svc.ResetPassword(context.Background(), u, "a-long-chosen-password")
	if err != nil {
		t.Fatal(err)
	}
	if res.Password != "" || res.SessionsEnded != 1 {
		t.Fatalf("result = %+v; a chosen password is not echoed back", res)
	}
	var stored models.User
	f.db.First(&stored, u.ID)
	if bcrypt.CompareHashAndPassword([]byte(stored.PasswordHash), []byte("a-long-chosen-password")) != nil || stored.MustChangePassword {
		t.Error("the chosen password was not set as the account's own")
	}
	if !stored.TwoFactorEnabled {
		t.Error("a password reset alone turned off two-factor")
	}
}

func TestResetTwoFactor(t *testing.T) {
	f := newFixture(t)
	u := f.lockedOutAdmin(t)

	changed, err := f.svc.ResetTwoFactor(u)
	if err != nil || !changed {
		t.Fatalf("changed %v, err %v", changed, err)
	}
	changed, err = f.svc.ResetTwoFactor(u)
	if err != nil || changed {
		t.Fatalf("second reset: changed %v, err %v; want a no-op", changed, err)
	}
}
