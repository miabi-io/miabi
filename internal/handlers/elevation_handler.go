// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/elevation"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// ElevationHandler serves the admin console unlock.
type ElevationHandler struct {
	svc   *elevation.Service
	users *repositories.UserRepository
	audit *audit.Logger
}

func NewElevationHandler(svc *elevation.Service, users *repositories.UserRepository, auditLog *audit.Logger) *ElevationHandler {
	return &ElevationHandler{svc: svc, users: users, audit: auditLog}
}

// ElevationState is what the console needs to decide whether to ask for a code.
type ElevationState struct {
	Required      bool       `json:"required"`
	Active        bool       `json:"active"`
	HasTwoFactor  bool       `json:"has_two_factor"`
	IPAllowed     bool       `json:"ip_allowed"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	IdleExpiresAt *time.Time `json:"idle_expires_at,omitempty"`
	LockedUntil   *time.Time `json:"locked_until,omitempty"`
	TTLSeconds    int        `json:"ttl_seconds"`
	IdleSeconds   int        `json:"idle_seconds"`
	// Token is returned only to a caller that authenticated with a bearer header (the CLI), which
	// sends it back in X-Miabi-Admin-Elevation. A browser gets the HttpOnly cookie instead.
	Token string `json:"token,omitempty"`
}

func (h *ElevationHandler) state(c *okapi.Context) ElevationState {
	set := h.svc.Settings()
	st := ElevationState{
		Required: set.Required, IPAllowed: elevation.IPAllowed(set, c.RealIP()),
		TTLSeconds: int(set.TTL.Seconds()), IdleSeconds: int(set.IdleTimeout.Seconds()),
	}
	uid := middlewares.UserID(c)
	if u, err := h.users.FindByID(uid); err == nil {
		st.HasTwoFactor = u.TwoFactorEnabled
	}
	if t := h.svc.LockedUntil(c.Request().Context(), uid); !t.IsZero() {
		st.LockedUntil = &t
	}
	if !set.Required {
		return st
	}
	if rec, err := h.svc.Peek(c.Request().Context(), uid, middlewares.SessionJTI(c), middlewares.UnlockToken(c)); err == nil {
		st.Active = true
		exp, idle := rec.ExpiresAt, rec.IdleExpiresAt(set.IdleTimeout)
		st.ExpiresAt, st.IdleExpiresAt = &exp, &idle
	}
	return st
}

// State reports whether the console needs an unlock and whether this session has one.
func (h *ElevationHandler) State(c *okapi.Context) error { return ok(c, h.state(c)) }

// ElevateRequest carries the second factor: a TOTP code or a recovery code.
type ElevateRequest struct {
	Body struct {
		Factor string `json:"factor" enum:"totp"`
		Code   string `json:"code" required:"true" maxLength:"64"`
	} `json:"body"`
}

// Elevate unlocks the admin console for this session. Every attempt is audited.
func (h *ElevationHandler) Elevate(c *okapi.Context, req *ElevateRequest) error {
	uid := middlewares.UserID(c)
	if middlewares.AuthMethod(c) == "api_key" {
		return c.AbortBadRequest("API keys do not need the console unlock")
	}
	if !elevation.IPAllowed(h.svc.Settings(), c.RealIP()) {
		return c.AbortForbidden(elevation.ErrIPNotAllowed.Error(), elevation.ErrIPNotAllowed)
	}
	user, err := h.users.FindByID(uid)
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	token, rec, err := h.svc.Elevate(c.Request().Context(), user, middlewares.SessionJTI(c),
		middlewares.SessionExpiry(c), strings.TrimSpace(req.Body.Code))
	if err != nil {
		action := "admin.elevate.failure"
		if errors.Is(err, elevation.ErrLocked) {
			action = "admin.elevate.lockout"
		}
		h.record(c, uid, action, map[string]any{"reason": err.Error()})
		switch {
		case errors.Is(err, elevation.ErrNotRequired):
			return c.AbortBadRequest(err.Error())
		case errors.Is(err, elevation.ErrLocked):
			return c.AbortWithError(http.StatusTooManyRequests, err)
		case errors.Is(err, elevation.ErrInvalidCode):
			return c.AbortBadRequest(err.Error(), err)
		case errors.Is(err, elevation.ErrSetupRequired), errors.Is(err, elevation.ErrRequired):
			return c.AbortForbidden(err.Error(), err)
		default:
			return c.AbortInternalServerError("failed to unlock the admin console", err)
		}
	}
	h.record(c, uid, "admin.elevate.success", map[string]any{"expires_at": rec.ExpiresAt})
	http.SetCookie(c.ResponseWriter(), &http.Cookie{
		Name: elevation.CookieName, Value: token,
		// /api/v1, not /api/v1/admin: admin endpoints also live under /nodes, /system and /clusters.
		Path:     "/api/v1",
		MaxAge:   int(time.Until(rec.ExpiresAt).Seconds()),
		HttpOnly: true, Secure: requestIsHTTPS(c), SameSite: http.SameSiteStrictMode,
	})
	st := h.state(c)
	st.Active = true
	exp, idle := rec.ExpiresAt, rec.IdleExpiresAt(h.svc.Settings().IdleTimeout)
	st.ExpiresAt, st.IdleExpiresAt = &exp, &idle
	if strings.TrimSpace(c.Header("Authorization")) != "" {
		st.Token = token
	}
	return ok(c, st)
}

// Lock ends this session's unlock.
func (h *ElevationHandler) Lock(c *okapi.Context) error {
	h.svc.Revoke(c.Request().Context(), middlewares.UnlockToken(c))
	http.SetCookie(c.ResponseWriter(), &http.Cookie{
		Name: elevation.CookieName, Value: "", Path: "/api/v1", MaxAge: -1,
		HttpOnly: true, Secure: requestIsHTTPS(c), SameSite: http.SameSiteStrictMode,
	})
	h.record(c, middlewares.UserID(c), "admin.elevate.lock", nil)
	return ok(c, h.state(c))
}

func (h *ElevationHandler) record(c *okapi.Context, uid uint, action string, meta map[string]any) {
	h.audit.Record(audit.Entry{ActorID: &uid, Action: action, TargetType: "user", IP: c.RealIP(), Metadata: meta})
}
