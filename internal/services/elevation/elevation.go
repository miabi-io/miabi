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

// Package elevation is the admin console unlock (sudo mode): a 2FA-proved token bound to the session jti.
package elevation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/secpolicy"
	"github.com/redis/go-redis/v9"
)

// CookieName carries the unlock for browser clients; HeaderName carries it for the CLI.
const (
	CookieName = "miabi_admin_unlock"
	HeaderName = "X-Miabi-Admin-Elevation"

	keyPrefix  = "admin:elev:"
	failPrefix = "admin:elev:fail:"
	lockPrefix = "admin:elev:lock:"
)

// A stable code lets the web client tell "unlock the console" from any other 403.
type CodedError struct{ code, msg string }

func (e *CodedError) Error() string { return e.msg }

// Code satisfies the error handler's Code() hook.
func (e *CodedError) Code() string { return e.code }

var (
	ErrRequired      = &CodedError{"ADMIN_ELEVATION_REQUIRED", "the admin console is locked: confirm your second factor to unlock it"}
	ErrSetupRequired = &CodedError{"ADMIN_2FA_SETUP_REQUIRED", "set up two-factor authentication before unlocking the admin console"}
	ErrStale         = &CodedError{"ADMIN_ELEVATION_STALE", "this action needs a recent unlock: confirm your second factor again"}
	ErrIPNotAllowed  = &CodedError{"ADMIN_IP_NOT_ALLOWED", "the admin console is not reachable from this address"}
	ErrLocked        = &CodedError{"ADMIN_ELEVATION_LOCKED", "too many wrong codes: the admin console unlock is locked for a while"}
	ErrInvalidCode   = &CodedError{"ADMIN_ELEVATION_INVALID_CODE", "invalid code"}
	ErrNotRequired   = errors.New("the admin console unlock is not enabled")
)

// PolicySource yields the Enterprise admin_access policy. Satisfied by *secpolicy.Service.
type PolicySource interface {
	AdminAccessPolicy() (secpolicy.AdminAccess, bool)
}

// SecondFactor verifies a TOTP or recovery code. Satisfied by *auth.Service.
type SecondFactor interface {
	VerifySecondFactor(user *models.User, code string) error
}

// Notifier tells the other admins about a lockout. Satisfied by *alerting.AdminNotifier.
type Notifier interface {
	NotifyAdmins(item models.Notification) error
}

// Record is an unlock as stored in Redis, keyed by the token's hash.
type Record struct {
	UserID    uint      `json:"user_id"`
	JTI       string    `json:"jti"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used"`
	ExpiresAt time.Time `json:"expires_at"`
}

// IdleExpiresAt is when the unlock lapses without further use.
func (r Record) IdleExpiresAt(idle time.Duration) time.Time {
	t := r.LastUsed.Add(idle)
	if t.After(r.ExpiresAt) {
		return r.ExpiresAt
	}
	return t
}

// Service issues and checks unlocks.
type Service struct {
	rdb       *redis.Client
	policy    PolicySource
	factor    SecondFactor
	notify    Notifier
	community bool
	now       func() time.Time
}

// community is MIABI_ADMIN_UNLOCK, Community's TOTP-unlock switch, superseded by the Enterprise policy.
func NewService(rdb *redis.Client, policy PolicySource, factor SecondFactor, community bool) *Service {
	return &Service{rdb: rdb, policy: policy, factor: factor, community: community, now: time.Now}
}

// SetNotifier wires lockout notices to the other admins.
func (s *Service) SetNotifier(n Notifier) { s.notify = n }

// Settings prefers the Enterprise policy, then Community's switch; an Enterprise allowlist may apply even if not required.
func (s *Service) Settings() secpolicy.AdminAccess {
	if s == nil {
		return secpolicy.AdminAccess{}
	}
	if s.policy != nil {
		if a, ok := s.policy.AdminAccessPolicy(); ok {
			return a
		}
	}
	if s.community {
		return secpolicy.DefaultAdminAccess()
	}
	return secpolicy.AdminAccess{}
}

// IPAllowed reports whether ip may reach the admin console under the settings.
func IPAllowed(set secpolicy.AdminAccess, ip string) bool {
	if len(set.AllowedIPs) == 0 {
		return true
	}
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range set.AllowedIPs {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Elevate's unlock never outlives sessionExp; the caller audits every attempt.
func (s *Service) Elevate(ctx context.Context, user *models.User, jti string, sessionExp time.Time, code string) (string, Record, error) {
	set := s.Settings()
	if !set.Required {
		return "", Record{}, ErrNotRequired
	}
	if !user.TwoFactorEnabled {
		return "", Record{}, ErrSetupRequired
	}
	if jti == "" {
		return "", Record{}, ErrRequired
	}
	if s.lockedUntil(ctx, user.ID).After(s.now()) {
		return "", Record{}, ErrLocked
	}
	if err := s.factor.VerifySecondFactor(user, code); err != nil {
		if s.fail(ctx, user, set) {
			return "", Record{}, ErrLocked
		}
		return "", Record{}, ErrInvalidCode
	}
	s.rdb.Del(ctx, failPrefix+strconv.FormatUint(uint64(user.ID), 10))

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", Record{}, err
	}
	token := hex.EncodeToString(raw)
	now := s.now()
	rec := Record{UserID: user.ID, JTI: jti, CreatedAt: now, LastUsed: now, ExpiresAt: now.Add(set.TTL)}
	if !sessionExp.IsZero() && sessionExp.Before(rec.ExpiresAt) {
		rec.ExpiresAt = sessionExp
	}
	if err := s.save(ctx, token, rec); err != nil {
		return "", Record{}, err
	}
	return token, rec, nil
}

func (s *Service) save(ctx context.Context, token string, rec Record) error {
	ttl := rec.ExpiresAt.Sub(s.now())
	if ttl <= 0 {
		return ErrRequired
	}
	b, _ := json.Marshal(rec)
	return s.rdb.Set(ctx, keyPrefix+hashToken(token), b, ttl).Err()
}

// fail counts a wrong code and reports whether it tripped the lockout.
func (s *Service) fail(ctx context.Context, user *models.User, set secpolicy.AdminAccess) bool {
	key := failPrefix + strconv.FormatUint(uint64(user.ID), 10)
	n, err := s.rdb.Incr(ctx, key).Result()
	if err != nil {
		return false
	}
	if n == 1 {
		s.rdb.Expire(ctx, key, set.Lockout)
	}
	if int(n) < set.MaxAttempts {
		return false
	}
	s.rdb.Del(ctx, key)
	until := s.now().Add(set.Lockout)
	s.rdb.Set(ctx, lockPrefix+strconv.FormatUint(uint64(user.ID), 10), until.Unix(), set.Lockout)
	if s.notify != nil {
		// The account itself stays usable; only the console unlock is locked.
		err := s.notify.NotifyAdmins(models.Notification{
			Category: models.CategorySecurity, Severity: models.AlertWarning,
			Title: "Admin console unlock locked — " + user.Email,
			Body: fmt.Sprintf("%d wrong codes in a row. The unlock is locked until %s; the account is not.",
				set.MaxAttempts, until.UTC().Format(time.RFC1123)),
			SubjectLink: fmt.Sprintf("/admin/users/%d", user.ID),
		})
		if err != nil {
			logger.Warn("admin unlock: notify admins of a lockout", "error", err)
		}
	}
	return true
}

func (s *Service) lockedUntil(ctx context.Context, userID uint) time.Time {
	v, err := s.rdb.Get(ctx, lockPrefix+strconv.FormatUint(uint64(userID), 10)).Int64()
	if err != nil {
		return time.Time{}
	}
	return time.Unix(v, 0)
}

func (s *Service) Check(ctx context.Context, userID uint, jti, token string) (Record, error) {
	rec, err := s.load(ctx, userID, jti, token)
	if err != nil {
		return Record{}, err
	}
	rec.LastUsed = s.now()
	if err := s.save(ctx, token, rec); err != nil {
		return Record{}, ErrRequired
	}
	return rec, nil
}

// Peek is Check without touching the idle window, for status reads.
func (s *Service) Peek(ctx context.Context, userID uint, jti, token string) (Record, error) {
	return s.load(ctx, userID, jti, token)
}

func (s *Service) load(ctx context.Context, userID uint, jti, token string) (Record, error) {
	if token == "" || jti == "" {
		return Record{}, ErrRequired
	}
	b, err := s.rdb.Get(ctx, keyPrefix+hashToken(token)).Bytes()
	if err != nil {
		return Record{}, ErrRequired
	}
	var rec Record
	if json.Unmarshal(b, &rec) != nil || rec.UserID != userID || rec.JTI != jti {
		return Record{}, ErrRequired
	}
	now := s.now()
	set := s.Settings()
	if !now.Before(rec.ExpiresAt) || (set.IdleTimeout > 0 && !now.Before(rec.IdleExpiresAt(set.IdleTimeout))) {
		s.rdb.Del(ctx, keyPrefix+hashToken(token))
		return Record{}, ErrRequired
	}
	return rec, nil
}

// Fresh reports whether rec is recent enough for a sensitive action.
func (s *Service) Fresh(rec Record) bool {
	w := s.Settings().SensitiveWindow
	return w <= 0 || s.now().Sub(rec.CreatedAt) <= w
}

// Revoke deletes an unlock.
func (s *Service) Revoke(ctx context.Context, token string) {
	if token != "" {
		s.rdb.Del(ctx, keyPrefix+hashToken(token))
	}
}

// ClearLockout drops a user's console-unlock lockout and failure count, for host-side recovery.
func ClearLockout(ctx context.Context, rdb *redis.Client, userID uint) error {
	id := strconv.FormatUint(uint64(userID), 10)
	return rdb.Del(ctx, lockPrefix+id, failPrefix+id).Err()
}

// LockedUntil reports when a user's unlock lockout ends; zero when not locked.
func (s *Service) LockedUntil(ctx context.Context, userID uint) time.Time {
	t := s.lockedUntil(ctx, userID)
	if t.Before(s.now()) {
		return time.Time{}
	}
	return t
}
