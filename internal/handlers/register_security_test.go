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

package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/registration"
	"github.com/miabi-io/miabi/internal/services/settings"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func registerTestHandler(t *testing.T, allowedDomains string) (*RegisterHandler, *repositories.UserRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.User{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	users := repositories.NewUserRepository(db)
	provider := settings.NewFixedProvider(map[string]models.Setting{
		settings.KeyAllowedSignupDomains: {Key: settings.KeyAllowedSignupDomains, Value: allowedDomains, Type: models.SettingTypeString},
	})
	return NewRegisterHandler(registration.NewService(true, provider, nil), nil, users, nil, nil), users
}

type registerResult struct {
	status  int
	body    string
	elapsed time.Duration
}

func doRegister(t *testing.T, h *RegisterHandler, name, email, password string) registerResult {
	t.Helper()
	rec := httptest.NewRecorder()
	c := okapi.NewContext(okapi.New(), rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", nil))
	req := &RegisterRequest{}
	req.Body.Name, req.Body.Email, req.Body.Password = name, email, password

	start := time.Now()
	if err := h.Register(c, req); err != nil {
		t.Fatalf("Register returned an error: %v", err)
	}
	return registerResult{status: rec.Code, body: rec.Body.String(), elapsed: time.Since(start)}
}

func TestDuplicateSignUpCostsTheSameAsAFreshOne(t *testing.T) {
	h, users := registerTestHandler(t, "")

	first := doRegister(t, h, "Ada", "ada@example.com", "correct-horse-battery")
	if first.status != http.StatusOK {
		t.Fatalf("first sign-up: status %d, body %s", first.status, first.body)
	}
	second := doRegister(t, h, "Imposter", "ada@example.com", "correct-horse-battery")

	if second.status != first.status || second.body != first.body {
		t.Errorf("a duplicate address answered differently:\n first  %d %s\n second %d %s",
			first.status, first.body, second.status, second.body)
	}
	// bcrypt at default cost cannot finish under 10ms, so faster proves the handler skipped hashing.
	if second.elapsed < 10*time.Millisecond {
		t.Errorf("the duplicate path took %v, too fast to have hashed a password — "+
			"the existence check has moved back in front of bcrypt", second.elapsed)
	}
	if n, _ := users.Count(); n != 1 {
		t.Errorf("user count = %d, want 1: the duplicate must not create a second account", n)
	}
}

// A rejected domain must answer like an accepted one, or the allow-list can be enumerated.
func TestRejectedDomainAnswersLikeSuccess(t *testing.T) {
	h, users := registerTestHandler(t, "acme.com")

	allowed := doRegister(t, h, "Ada", "ada@acme.com", "correct-horse-battery")
	if allowed.status != http.StatusOK {
		t.Fatalf("allowed domain: status %d, body %s", allowed.status, allowed.body)
	}
	rejected := doRegister(t, h, "Eve", "eve@evil.example", "correct-horse-battery")

	if rejected.status != allowed.status || rejected.body != allowed.body {
		t.Errorf("a rejected domain answered differently:\n allowed  %d %s\n rejected %d %s",
			allowed.status, allowed.body, rejected.status, rejected.body)
	}
	if rejected.elapsed < 10*time.Millisecond {
		t.Errorf("the rejected-domain path took %v, too fast to have hashed a password", rejected.elapsed)
	}
	if _, err := users.FindByEmail("eve@evil.example"); err == nil {
		t.Error("a rejected domain created an account")
	}
}

// Closed sign-up is public (the sign-in page must know), so it may look like a refusal.
func TestClosedPlatformRefusesOutright(t *testing.T) {
	h := NewRegisterHandler(registration.NewService(false, settings.NewFixedProvider(nil), nil), nil, nil, nil, nil)

	rec := httptest.NewRecorder()
	c := okapi.NewContext(okapi.New(), rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", nil))
	req := &RegisterRequest{}
	req.Body.Name, req.Body.Email, req.Body.Password = "Eve", "eve@example.com", "correct-horse-battery"

	_ = h.Register(c, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 when sign-up is off", rec.Code)
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "domain") {
		t.Errorf("the refusal mentioned domains: %s", rec.Body.String())
	}
}
