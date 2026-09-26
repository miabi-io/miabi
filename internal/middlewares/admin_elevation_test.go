// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package middlewares

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/elevation"
	"github.com/redis/go-redis/v9"
)

type okFactor struct{}

func (okFactor) VerifySecondFactor(_ *models.User, code string) error {
	if code == "123456" {
		return nil
	}
	return errors.New("bad code")
}

// adminCall drives one request through the unlock check the way RequireSystemAdmin runs it.
func adminCall(t *testing.T, gate *elevation.Service, user *models.User, authMethod, jti, unlock string) (int, bool) {
	t.Helper()
	reached := false
	app := okapi.New()
	seed := func(c *okapi.Context) error {
		c.Set(CtxAuthMethod, authMethod)
		c.Set(CtxUserID, int(user.ID))
		c.Set(ctxJTI, jti)
		return c.Next()
	}
	guard := func(c *okapi.Context) error {
		if err := checkElevation(c, gate, user); err != nil {
			return c.AbortForbidden(err.Error(), err)
		}
		return c.Next()
	}
	app.Get("/api/v1/admin/thing", func(c *okapi.Context) error {
		reached = true
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	}, okapi.UseMiddleware(seed), okapi.UseMiddleware(guard))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/thing", nil)
	if unlock != "" {
		req.AddCookie(&http.Cookie{Name: elevation.CookieName, Value: unlock})
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	return rec.Code, reached
}

func TestAdminConsoleNeedsAnUnlock(t *testing.T) {
	mr := miniredis.RunT(t)
	gate := elevation.NewService(redis.NewClient(&redis.Options{Addr: mr.Addr()}), nil, okFactor{}, true)
	admin := &models.User{ID: 1, Role: models.SystemRoleAdmin, TwoFactorEnabled: true}

	if code, reached := adminCall(t, gate, admin, "jwt", "jti-1", ""); code != http.StatusForbidden || reached {
		t.Fatalf("no unlock: status %d reached %v, want 403 and not reached", code, reached)
	}
	token, _, err := gate.Elevate(context.Background(), admin, "jti-1", time.Now().Add(time.Hour), "123456")
	if err != nil {
		t.Fatal(err)
	}
	if code, reached := adminCall(t, gate, admin, "jwt", "jti-1", token); code != http.StatusOK || !reached {
		t.Fatalf("with an unlock: status %d reached %v", code, reached)
	}
	// After sign-out the session's jti is gone; a replayed unlock from another session is refused.
	if code, _ := adminCall(t, gate, admin, "jwt", "jti-2", token); code != http.StatusForbidden {
		t.Fatalf("unlock replayed on another session: status %d", code)
	}
	if code, reached := adminCall(t, gate, admin, "api_key", "", ""); code != http.StatusOK || !reached {
		t.Fatalf("an admin API key was asked for an unlock: status %d", code)
	}
	noTOTP := &models.User{ID: 2, Role: models.SystemRoleAdmin}
	if code, _ := adminCall(t, gate, noTOTP, "jwt", "jti-3", ""); code != http.StatusForbidden {
		t.Fatalf("an admin without 2FA got in: status %d", code)
	}
}

func TestAdminConsoleUnlockOffByDefault(t *testing.T) {
	mr := miniredis.RunT(t)
	gate := elevation.NewService(redis.NewClient(&redis.Options{Addr: mr.Addr()}), nil, okFactor{}, false)
	admin := &models.User{ID: 1, Role: models.SystemRoleAdmin}
	if code, reached := adminCall(t, gate, admin, "jwt", "jti", ""); code != http.StatusOK || !reached {
		t.Fatalf("status %d: an upgrade must not ask admins for a code until the unlock is turned on", code)
	}
}
