// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package elevation

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/secpolicy"
	"github.com/redis/go-redis/v9"
)

type codeFactor struct{ good string }

func (f codeFactor) VerifySecondFactor(_ *models.User, code string) error {
	if code == f.good {
		return nil
	}
	return errors.New("bad code")
}

type fixedPolicy struct {
	a  secpolicy.AdminAccess
	ok bool
}

func (p fixedPolicy) AdminAccessPolicy() (secpolicy.AdminAccess, bool) { return p.a, p.ok }

type recordingNotifier struct{ items []models.Notification }

func (n *recordingNotifier) NotifyAdmins(item models.Notification) error {
	n.items = append(n.items, item)
	return nil
}

func newTest(t *testing.T, policy PolicySource, community bool) (*Service, *miniredis.Miniredis, *time.Time) {
	t.Helper()
	mr := miniredis.RunT(t)
	svc := NewService(redis.NewClient(&redis.Options{Addr: mr.Addr()}), policy, codeFactor{"123456"}, community)
	now := time.Now()
	svc.now = func() time.Time { return now }
	return svc, mr, &now
}

var admin = &models.User{ID: 1, Email: "root@example.com", TwoFactorEnabled: true}

func TestSettingsPrecedence(t *testing.T) {
	off, _, _ := newTest(t, fixedPolicy{}, false)
	if off.Settings().Required {
		t.Error("unlock required with neither the switch nor a policy")
	}
	ce, _, _ := newTest(t, fixedPolicy{}, true)
	if got := ce.Settings(); !got.Required || got.TTL != 30*time.Minute {
		t.Errorf("community settings = %+v", got)
	}
	ee, _, _ := newTest(t, fixedPolicy{a: secpolicy.AdminAccess{Required: true, TTL: time.Hour}, ok: true}, true)
	if got := ee.Settings(); got.TTL != time.Hour {
		t.Errorf("an enforced policy did not supersede the Community switch: %+v", got)
	}
}

func TestUnlockIsBoundToTheSession(t *testing.T) {
	svc, _, _ := newTest(t, fixedPolicy{}, true)
	ctx := context.Background()
	token, _, err := svc.Elevate(ctx, admin, "jti-a", time.Time{}, "123456")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Check(ctx, 1, "jti-a", token); err != nil {
		t.Fatalf("own session: %v", err)
	}
	if _, err := svc.Check(ctx, 1, "jti-b", token); !errors.Is(err, ErrRequired) {
		t.Error("an unlock was accepted with another session")
	}
	if _, err := svc.Check(ctx, 2, "jti-a", token); !errors.Is(err, ErrRequired) {
		t.Error("an unlock was accepted for another user")
	}
	svc.Revoke(ctx, token)
	if _, err := svc.Check(ctx, 1, "jti-a", token); !errors.Is(err, ErrRequired) {
		t.Error("a revoked unlock still worked")
	}
}

func TestUnlockExpiresOnIdleAndNeverOutlivesTheSession(t *testing.T) {
	svc, _, now := newTest(t, fixedPolicy{}, true)
	ctx := context.Background()
	token, rec, err := svc.Elevate(ctx, admin, "jti", now.Add(20*time.Minute), "123456")
	if err != nil {
		t.Fatal(err)
	}
	if !rec.ExpiresAt.Equal(now.Add(20 * time.Minute)) {
		t.Errorf("unlock expires %v, want the session's expiry", rec.ExpiresAt)
	}
	*now = now.Add(9 * time.Minute)
	if _, err := svc.Check(ctx, 1, "jti", token); err != nil {
		t.Fatalf("within the idle window: %v", err)
	}
	*now = now.Add(11 * time.Minute)
	if _, err := svc.Check(ctx, 1, "jti", token); !errors.Is(err, ErrRequired) {
		t.Error("an idle unlock was still accepted")
	}
}

func TestSensitiveWindow(t *testing.T) {
	svc, _, now := newTest(t, fixedPolicy{}, true)
	rec := Record{CreatedAt: *now}
	if !svc.Fresh(rec) {
		t.Error("a new unlock is not fresh")
	}
	*now = now.Add(6 * time.Minute)
	if svc.Fresh(rec) {
		t.Error("a six-minute-old unlock passed the five-minute window")
	}
}

// Wrong codes lock the unlock, not the account, and tell the other admins.
func TestLockoutAfterMaxAttempts(t *testing.T) {
	svc, _, _ := newTest(t, fixedPolicy{a: secpolicy.AdminAccess{Required: true, TTL: time.Hour, IdleTimeout: time.Hour, MaxAttempts: 3, Lockout: 15 * time.Minute}, ok: true}, false)
	n := &recordingNotifier{}
	svc.SetNotifier(n)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, _, err := svc.Elevate(ctx, admin, "jti", time.Time{}, "000000"); !errors.Is(err, ErrInvalidCode) {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	if _, _, err := svc.Elevate(ctx, admin, "jti", time.Time{}, "000000"); !errors.Is(err, ErrLocked) {
		t.Fatalf("third wrong code: %v, want ErrLocked", err)
	}
	if _, _, err := svc.Elevate(ctx, admin, "jti", time.Time{}, "123456"); !errors.Is(err, ErrLocked) {
		t.Fatal("a correct code got through a lockout")
	}
	if len(n.items) != 1 {
		t.Errorf("notified %d times, want 1", len(n.items))
	}
}

func TestSetupRequiredWithoutTwoFactor(t *testing.T) {
	svc, _, _ := newTest(t, fixedPolicy{}, true)
	_, _, err := svc.Elevate(context.Background(), &models.User{ID: 3}, "jti", time.Time{}, "123456")
	if !errors.Is(err, ErrSetupRequired) {
		t.Fatalf("err = %v, want ErrSetupRequired", err)
	}
}

func TestIPAllowed(t *testing.T) {
	set := secpolicy.AdminAccess{AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("2001:db8::1/128")}}
	for ip, want := range map[string]bool{"10.1.2.3": true, "::ffff:10.1.2.3": true, "192.168.1.1": false, "2001:db8::1": true, "garbage": false} {
		if got := IPAllowed(set, ip); got != want {
			t.Errorf("IPAllowed(%q) = %v, want %v", ip, got, want)
		}
	}
	if !IPAllowed(secpolicy.AdminAccess{}, "192.168.1.1") {
		t.Error("an empty allowlist refused an address")
	}
}

// The error envelope reads the code off the error, which is what the web client keys on.
func TestErrorsCarryMachineCodes(t *testing.T) {
	var coder interface{ Code() string }
	if !errors.As(error(ErrRequired), &coder) || coder.Code() != "ADMIN_ELEVATION_REQUIRED" {
		t.Fatal("ErrRequired does not expose its code")
	}
}
