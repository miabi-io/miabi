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

package oauth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func seedUser(t *testing.T, db *gorm.DB, email string) *models.User {
	t.Helper()
	u := &models.User{Name: "Admin", Email: email, PasswordHash: "x", Role: models.SystemRoleAdmin, Active: true}
	if err := db.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	return u
}

func authenticate(s *Service, idp *stubIdP) (*models.User, error) {
	return s.Authenticate(context.Background(), providerFor(idp, nil), "code", "http://localhost/cb")
}

// An attacker who puts the admin's address on their own IdP account must not be signed in as the admin.
func TestAuthenticate_RefusesToLinkAnUnverifiedEmail(t *testing.T) {
	for name, verified := range map[string]any{"false": false, "string false": "false", "absent": nil} {
		t.Run(name, func(t *testing.T) {
			s, db, idp := registerService(t, "admin@acme.test")
			seedUser(t, db, "admin@acme.test")
			claims := verifiedClaims("attacker", "admin@acme.test")
			if verified == nil {
				delete(claims, "email_verified")
			} else {
				claims["email_verified"] = verified
			}
			idp.assert(claims)

			if _, err := authenticate(s, idp); !errors.Is(err, ErrEmailNotVerified) {
				t.Fatalf("err = %v, want ErrEmailNotVerified", err)
			}
			var n int64
			db.Model(&models.UserIdentity{}).Count(&n)
			if n != 0 {
				t.Fatalf("%d identities linked, want none", n)
			}
		})
	}
}

// A directory that never sends email_verified can be trusted explicitly by an admin.
func TestAuthenticate_LinksAnUnverifiedEmailWhenTheProviderAllowsIt(t *testing.T) {
	s, db, idp := registerService(t, "admin@acme.test")
	admin := seedUser(t, db, "admin@acme.test")
	idp.assert(map[string]any{"sub": "admin-sub", "email": "admin@acme.test"})
	p := providerFor(idp, nil)
	p.AllowUnverifiedEmail = true

	u, err := s.Authenticate(context.Background(), p, "code", "http://localhost/cb")
	if err != nil || u.ID != admin.ID {
		t.Fatalf("got user %v, err %v; want the admin linked", u, err)
	}
}

// Some providers send the claim as a string; "true" still links.
func TestAuthenticate_LinksAVerifiedEmailSentAsAString(t *testing.T) {
	s, db, idp := registerService(t, "admin@acme.test")
	admin := seedUser(t, db, "admin@acme.test")
	claims := verifiedClaims("admin-sub", "admin@acme.test")
	claims["email_verified"] = "true"
	idp.assert(claims)

	u, err := authenticate(s, idp)
	if err != nil || u.ID != admin.ID {
		t.Fatalf("got user %v, err %v; want the admin linked", u, err)
	}
}

// Once linked, a returning user is found by subject even after the provider-side email changes.
func TestAuthenticate_MatchesAReturningUserBySubject(t *testing.T) {
	s, db, idp := registerService(t, "admin@acme.test")
	admin := seedUser(t, db, "admin@acme.test")
	idp.assert(verifiedClaims("admin-sub", "admin@acme.test"))
	if _, err := authenticate(s, idp); err != nil {
		t.Fatalf("first sign-in: %v", err)
	}

	idp.assert(map[string]any{"sub": "admin-sub", "email": "renamed@acme.test", "email_verified": false})
	u, err := authenticate(s, idp)
	if err != nil || u.ID != admin.ID {
		t.Fatalf("got user %v, err %v; want the admin by subject", u, err)
	}
}

// A second IdP account with the same verified email cannot attach to an account already linked.
func TestAuthenticate_RefusesASecondIdentityForALinkedAccount(t *testing.T) {
	s, db, idp := registerService(t, "admin@acme.test")
	seedUser(t, db, "admin@acme.test")
	idp.assert(verifiedClaims("admin-sub", "admin@acme.test"))
	if _, err := authenticate(s, idp); err != nil {
		t.Fatalf("first sign-in: %v", err)
	}

	idp.assert(verifiedClaims("other-sub", "admin@acme.test"))
	if _, err := authenticate(s, idp); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("err = %v, want ErrIdentityConflict", err)
	}
}

func TestAuthenticate_RequiresASubject(t *testing.T) {
	s, _, idp := registerService(t, "new@acme.test")
	idp.assert(map[string]any{"email": "new@acme.test", "email_verified": true})
	if _, err := authenticate(s, idp); !errors.Is(err, ErrNoSubject) {
		t.Fatalf("err = %v, want ErrNoSubject", err)
	}
}

// A registration through a provider that does not verify the email leaves the account unverified.
func TestAuthenticate_RegistersAnUnverifiedEmailAsUnverified(t *testing.T) {
	s, _, idp := registerService(t, "new@acme.test")
	idp.assert(map[string]any{"sub": "new-sub", "email": "new@acme.test"})
	u, err := authenticate(s, idp)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if u.EmailVerifiedAt != nil {
		t.Fatal("an email the provider did not verify was marked verified")
	}
}

func twoFactorService(t *testing.T) *Service {
	t.Helper()
	mr := miniredis.RunT(t)
	return &Service{redis: redis.NewClient(&redis.Options{Addr: mr.Addr()})}
}

func TestTwoFactorChallengeIsSingleUse(t *testing.T) {
	ctx := context.Background()
	s := twoFactorService(t)
	id, err := s.BeginTwoFactor(ctx, TwoFactorChallenge{UserID: 4, Provider: "acme-sso", Intent: "login_token"})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := s.PendingTwoFactor(ctx, id)
	if err != nil || ch.UserID != 4 || ch.Provider != "acme-sso" || ch.Intent != "login_token" {
		t.Fatalf("pending = %+v, %v", ch, err)
	}
	if !s.EndTwoFactor(ctx, id) {
		t.Fatal("first completion refused")
	}
	if s.EndTwoFactor(ctx, id) {
		t.Fatal("a challenge completed twice")
	}
	if _, err := s.PendingTwoFactor(ctx, id); !errors.Is(err, ErrTwoFactorExpired) {
		t.Fatalf("err = %v, want ErrTwoFactorExpired", err)
	}
}

func TestTwoFactorChallengeIsDroppedAfterTooManyWrongCodes(t *testing.T) {
	ctx := context.Background()
	s := twoFactorService(t)
	id, _ := s.BeginTwoFactor(ctx, TwoFactorChallenge{UserID: 4})
	for i := 0; i < twoFactorMaxAttempts-1; i++ {
		s.FailTwoFactor(ctx, id)
	}
	if _, err := s.PendingTwoFactor(ctx, id); err != nil {
		t.Fatalf("dropped before the last attempt: %v", err)
	}
	s.FailTwoFactor(ctx, id)
	if _, err := s.PendingTwoFactor(ctx, id); !errors.Is(err, ErrTwoFactorExpired) {
		t.Fatalf("err = %v, want the challenge dropped", err)
	}
}

// Only a flow that needs a fresh IdP login asks for one: a landing path rides in the intent but
// must not make someone already signed in at the IdP sign in again.
func TestAuthCodeURLPrompt(t *testing.T) {
	ctx := context.Background()
	s := twoFactorService(t)
	p := &models.OAuthProvider{Name: "console", ClientID: "c", AuthURL: "https://idp.example/authorize"}
	for _, tc := range []struct {
		intent string
		force  bool
	}{{"", false}, {"next\x1f/?workspace=prod", false}, {"login_token", true}} {
		u, state, err := s.AuthCodeURLWithIntent(ctx, p, "https://paas.example/cb", tc.intent, tc.force)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(u, "prompt=login"); got != tc.force {
			t.Errorf("intent %q: prompt=login %v, want %v (%s)", tc.intent, got, tc.force, u)
		}
		if got := s.ConsumeIntent(ctx, state); got != tc.intent {
			t.Errorf("intent %q read back as %q", tc.intent, got)
		}
	}
}
