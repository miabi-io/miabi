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

// Package adminrecovery is the audited break-glass path; shell access to the control plane is the trust boundary.
package adminrecovery

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/elevation"
	"github.com/miabi-io/miabi/internal/services/session"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

var (
	ErrNoUser   = errors.New("no user with that email")
	ErrNotAdmin = errors.New("user is not a platform admin")
	// ErrWeakPassword is models.ErrPasswordTooShort, the console's own rule.
	ErrWeakPassword = models.ErrPasswordTooShort
)

// Notifier tells the platform admins about a recovery. Satisfied by *alerting.AdminNotifier.
type Notifier interface {
	NotifyAdmins(item models.Notification) error
}

type Service struct {
	users         *repositories.UserRepository
	sessions      *repositories.SessionRepository
	recoveryCodes *repositories.TwoFactorRecoveryRepository
	audit         *repositories.AuditLogRepository
	notify        Notifier
	// Nil-safe: without Redis, sessions still end via the password fingerprint and the unlock lockout just expires.
	rdb *redis.Client
}

func NewService(db *gorm.DB, rdb *redis.Client, notify Notifier) *Service {
	return &Service{
		users:         repositories.NewUserRepository(db),
		sessions:      repositories.NewSessionRepository(db),
		recoveryCodes: repositories.NewTwoFactorRecoveryRepository(db),
		audit:         repositories.NewAuditLogRepository(db),
		notify:        notify,
		rdb:           rdb,
	}
}

// Admin returns the platform admin with this email.
func (s *Service) Admin(email string) (*models.User, error) {
	u, err := s.User(email)
	if err != nil {
		return nil, err
	}
	if !u.IsAdmin() {
		return nil, ErrNotAdmin
	}
	return u, nil
}

// User returns any user with this email.
func (s *Service) User(email string) (*models.User, error) {
	u, err := s.users.FindByEmail(strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		return nil, ErrNoUser
	}
	return u, nil
}

// Result reports what a recovery changed.
type Result struct {
	// Password is the new password; generated ones must be changed at the next sign-in.
	Password         string
	TwoFactorRemoved bool
	Reactivated      bool
	SessionsEnded    int
}

// Every session is ended since a lockout can be a sign the account was taken over.
func (s *Service) Recover(ctx context.Context, u *models.User) (Result, error) {
	password, err := generatePassword()
	if err != nil {
		return Result{}, err
	}
	res := Result{Password: password, TwoFactorRemoved: u.TwoFactorEnabled, Reactivated: !u.Active}
	if err := s.setPassword(u, password, true); err != nil {
		return Result{}, err
	}
	u.Active = true
	if err := s.clearTwoFactor(u); err != nil {
		return Result{}, err
	}
	if s.rdb != nil {
		_ = elevation.ClearLockout(ctx, s.rdb, u.ID)
	}
	if res.SessionsEnded, err = s.endSessions(ctx, u.ID); err != nil {
		return Result{}, err
	}
	s.record(u, "admin.break_glass", map[string]any{
		"password_reset": true, "two_factor_removed": res.TwoFactorRemoved, "reactivated": res.Reactivated,
	})
	s.announce(u, "Admin account recovered from the host — "+u.Email,
		"Its password was reset, two-factor authentication was turned off and its sessions were ended, from a shell in the control-plane container.")
	return res, nil
}

// An empty password generates one that must be changed at the next sign-in.
func (s *Service) ResetPassword(ctx context.Context, u *models.User, password string) (Result, error) {
	generated := password == ""
	if generated {
		var err error
		if password, err = generatePassword(); err != nil {
			return Result{}, err
		}
	} else if err := models.CheckNewPassword(password); err != nil {
		return Result{}, err
	}
	if err := s.setPassword(u, password, generated); err != nil {
		return Result{}, err
	}
	if err := s.users.Update(u); err != nil {
		return Result{}, err
	}
	ended, err := s.endSessions(ctx, u.ID)
	if err != nil {
		return Result{}, err
	}
	s.record(u, "user.password_reset_from_host", nil)
	s.announce(u, "Admin password reset from the host — "+u.Email,
		"The password was reset and its sessions were ended, from a shell in the control-plane container.")
	res := Result{SessionsEnded: ended}
	if generated {
		res.Password = password
	}
	return res, nil
}

// Reports false when two-factor was not on.
func (s *Service) ResetTwoFactor(u *models.User) (bool, error) {
	if !u.TwoFactorEnabled {
		return false, nil
	}
	if err := s.clearTwoFactor(u); err != nil {
		return false, err
	}
	s.record(u, "user.2fa_reset_from_host", nil)
	if u.IsAdmin() {
		s.announce(u, "Admin two-factor reset from the host — "+u.Email,
			"Two-factor authentication was turned off from a shell in the control-plane container. It must be set up again at the next admin sign-in.")
	}
	return true, nil
}

func (s *Service) setPassword(u *models.User, password string, mustChange bool) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.PasswordHash = string(hash)
	u.MustChangePassword = mustChange
	return nil
}

// clearTwoFactor persists u with two-factor off, along with any other pending change to it.
func (s *Service) clearTwoFactor(u *models.User) error {
	u.TwoFactorEnabled = false
	u.TwoFactorSecret = ""
	if err := s.users.Update(u); err != nil {
		return err
	}
	return s.recoveryCodes.DeleteForUser(u.ID)
}

// The new password already invalidates tokens; this also marks them revoked where the console lists them.
func (s *Service) endSessions(ctx context.Context, userID uint) (int, error) {
	list, err := s.sessions.ListByUser(userID)
	if err != nil {
		return 0, err
	}
	store := session.NewStore(s.rdb)
	n := 0
	for _, sess := range list {
		if sess.Revoked || sess.ExpiresAt.Before(time.Now()) {
			continue
		}
		n++
		if s.rdb != nil {
			store.MarkRevoked(ctx, sess.JTI, sess.ExpiresAt)
		}
	}
	return n, s.sessions.RevokeAllForUser(userID, "")
}

func (s *Service) record(u *models.User, action string, meta map[string]any) {
	if meta == nil {
		meta = map[string]any{}
	}
	meta["via"] = "host-cli"
	actor := u.ID
	_ = s.audit.Create(&models.AuditLog{
		ActorID: &actor, Action: action, TargetType: "user", TargetID: fmt.Sprint(u.ID), Metadata: meta,
	})
}

func (s *Service) announce(u *models.User, title, body string) {
	if s.notify == nil {
		return
	}
	_ = s.notify.NotifyAdmins(models.Notification{
		Category: models.CategorySecurity, Severity: models.AlertWarning, Title: title, Body: body,
	})
}

// No look-alike characters, since it is read off a terminal and typed.
func generatePassword() (string, error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	out := make([]byte, 20)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out), nil
}
