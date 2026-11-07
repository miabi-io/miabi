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

package middlewares

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/auth"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const testSecret = "a-test-jwt-secret-that-is-long-enough-1234"

type sessionHarness struct {
	db    *gorm.DB
	users *repositories.UserRepository
	auth  *auth.Service
	app   *okapi.Okapi
}

func newSessionHarness(t *testing.T) *sessionHarness {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.User{}); err != nil {
		t.Fatal(err)
	}
	users := repositories.NewUserRepository(db)
	jwtAuth := JWTAuth(&config.Config{JWTSecret: testSecret}, nil, users)
	app := okapi.New()
	app.Get("/me", func(c *okapi.Context) error {
		return c.JSON(http.StatusOK, map[string]uint{"user": UserID(c)})
	}, okapi.UseMiddleware(jwtAuth.Middleware))
	return &sessionHarness{db: db, users: users, auth: auth.NewService(users, nil, nil, nil, testSecret), app: app}
}

func (h *sessionHarness) user(t *testing.T, email, hash string) *models.User {
	t.Helper()
	u := &models.User{Name: email, Username: email, Email: email, PasswordHash: hash, Active: true, Role: models.SystemRoleUser}
	if err := h.db.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	return u
}

func (h *sessionHarness) call(token string) int {
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.app.ServeHTTP(rec, req)
	return rec.Code
}

func (h *sessionHarness) token(t *testing.T, u *models.User) string {
	t.Helper()
	tok, _, err := h.auth.IssueToken(u)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestSessionAcceptedForActiveUser(t *testing.T) {
	h := newSessionHarness(t)
	u := h.user(t, "ada@example.com", "hash-1")
	if code := h.call(h.token(t, u)); code != http.StatusOK {
		t.Fatalf("an active user's session = %d, want 200", code)
	}
}

// Disabling an account must end its sessions at once, not when the 24 h token expires.
func TestSessionEndsWhenUserDisabled(t *testing.T) {
	h := newSessionHarness(t)
	u := h.user(t, "ada@example.com", "hash-1")
	tok := h.token(t, u)
	h.db.Model(u).Update("active", false)
	if code := h.call(tok); code != http.StatusUnauthorized {
		t.Fatalf("a disabled user's session = %d, want 401", code)
	}
}

// Any password change, by the user, an emailed reset or an admin, ends every older session.
func TestSessionEndsWhenPasswordChanges(t *testing.T) {
	h := newSessionHarness(t)
	u := h.user(t, "ada@example.com", "hash-1")
	old := h.token(t, u)
	h.db.Model(u).Update("password_hash", "hash-2")
	if code := h.call(old); code != http.StatusUnauthorized {
		t.Fatalf("a session from before the password change = %d, want 401", code)
	}
	u.PasswordHash = "hash-2"
	if code := h.call(h.token(t, u)); code != http.StatusOK {
		t.Fatalf("a session issued after the change = %d, want 200", code)
	}
}

func TestSessionEndsWhenUserDeleted(t *testing.T) {
	h := newSessionHarness(t)
	u := h.user(t, "ada@example.com", "hash-1")
	tok := h.token(t, u)
	h.db.Delete(u)
	if code := h.call(tok); code != http.StatusUnauthorized {
		t.Fatalf("a deleted user's session = %d, want 401", code)
	}
}

// Fingerprint-less tokens stay valid until expiry, but a disabled account still ends them.
func TestLegacyTokenWithoutFingerprint(t *testing.T) {
	h := newSessionHarness(t)
	u := h.user(t, "ada@example.com", "hash-1")
	legacy, err := okapi.GenerateJwtToken([]byte(testSecret), jwt.MapClaims{
		"sub": u.ID, "email": u.Email, "role": string(u.Role), "aud": "miabi", "jti": "legacy-jti",
	}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if code := h.call(legacy); code != http.StatusOK {
		t.Fatalf("a legacy token for an active user = %d, want 200", code)
	}
	h.db.Model(u).Update("active", false)
	if code := h.call(legacy); code != http.StatusUnauthorized {
		t.Fatalf("a legacy token for a disabled user = %d, want 401", code)
	}
}

// The fingerprint is keyed: it depends on the secret, so it reveals nothing reusable about the hash.
func TestSessionFingerprintIsKeyed(t *testing.T) {
	a := auth.SessionFingerprint([]byte("secret-a"), "hash")
	if a == auth.SessionFingerprint([]byte("secret-b"), "hash") {
		t.Error("the fingerprint must depend on the key")
	}
	if a == auth.SessionFingerprint([]byte("secret-a"), "other-hash") {
		t.Error("the fingerprint must change with the password hash")
	}
}
