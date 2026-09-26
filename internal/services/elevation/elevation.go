// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package elevation is the admin console unlock ("sudo mode"): signing in gives an admin the
// workspace side, and the platform console additionally needs a short-lived unlock proved with a
// second factor. The unlock is an opaque, revocable token bound to the login session's jti, so a
// stolen unlock is useless without the session, and signing out or revoking the session kills it.
//
// It protects against an unattended browser, a stolen session cookie or a phished password alone.
// It does not protect against XSS on the same origin, which can ride an unlocked session.
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

// CodedError carries a stable machine code into the API error envelope, so the web client can tell
// "unlock the console" from any other 403 without parsing messages.
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

// NewService builds the unlock service. community is MIABI_ADMIN_UNLOCK: Community's switch for
// the TOTP unlock with default timings, which the Enterprise policy supersedes.
func NewService(rdb *redis.Client, policy PolicySource, factor SecondFactor, community bool) *Service {
	return &Service{rdb: rdb, policy: policy, factor: factor, community: community, now: time.Now}
}

// SetNotifier wires lockout notices to the other admins.
func (s *Service) SetNotifier(n Notifier) { s.notify = n }

// Settings resolves the unlock in force: the Enterprise policy when enforced, else Community's
// switch with default timings, else none. Required=false means the console needs no unlock, though
// an Enterprise allowlist may still apply.
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

// Elevate verifies a second factor and issues an unlock bound to the session jti. The unlock never
// outlives sessionExp. Every attempt is reported to audit by the caller.
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
			Title:    "Admin console unlock locked — " + user.Email,
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

// Check validates an unlock for the session and extends its idle window. It returns ErrRequired for
// anything that is not a live unlock of this user's this session.
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

// LockedUntil reports when a user's unlock lockout ends; zero when not locked.
func (s *Service) LockedUntil(ctx context.Context, userID uint) time.Time {
	t := s.lockedUntil(ctx, userID)
	if t.Before(s.now()) {
		return time.Time{}
	}
	return t
}
