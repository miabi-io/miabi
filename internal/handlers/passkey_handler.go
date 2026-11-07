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
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/auth"
	"github.com/miabi-io/miabi/internal/services/passkey"
	"github.com/miabi-io/miabi/internal/services/settings"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// The repository is still used to report and clean up existing passkeys when the service is nil.
func (h *AuthHandler) SetPasskeys(svc *passkey.Service, repo *repositories.PasskeyRepository) {
	h.passkeys, h.passkeyRepo = svc, repo
}

type PasskeyBegin struct {
	Token   string `json:"token"`
	Options any    `json:"options"`
}

// PasskeyLoginRequest answers a sign-in ceremony with the browser's assertion.
type PasskeyLoginRequest struct {
	Body struct {
		Token      string          `json:"token" required:"true" maxLength:"64"`
		Credential json.RawMessage `json:"credential"`
	} `json:"body"`
}

type PasskeyRegisterBeginRequest struct {
	Body struct {
		Password      string `json:"password" required:"true"`
		TwoFactorCode string `json:"two_factor_code,omitempty" maxLength:"64"`
	} `json:"body"`
}

// PasskeyRegisterRequest answers a registration ceremony with the new credential.
type PasskeyRegisterRequest struct {
	Body struct {
		Token      string          `json:"token" required:"true" maxLength:"64"`
		Name       string          `json:"name,omitempty" maxLength:"64"`
		Credential json.RawMessage `json:"credential"`
	} `json:"body"`
}

// PasskeyRenameRequest relabels a passkey.
type PasskeyRenameRequest struct {
	Body struct {
		Name string `json:"name" required:"true" minLength:"1" maxLength:"64"`
	} `json:"body"`
}

// PasskeyRemoveRequest confirms the password before a passkey is removed.
type PasskeyRemoveRequest struct {
	Body struct {
		Password string `json:"password" required:"true"`
	} `json:"body"`
}

var errPasskeyNeedsPassword = errors.New("passkeys are available to accounts that sign in with a password")

func (h *AuthHandler) passkeyAbort(c *okapi.Context, err error, what string) error {
	switch {
	case errors.Is(err, passkey.ErrDisabled):
		return c.AbortNotFound(err.Error())
	case errors.Is(err, passkey.ErrCeremony), errors.Is(err, passkey.ErrInvalid):
		return c.AbortBadRequest(err.Error())
	case errors.Is(err, passkey.ErrTooMany):
		return c.AbortConflict(err.Error())
	default:
		return c.AbortInternalServerError(what, err)
	}
}

// An SSO-only account has no password to confirm, and so cannot manage passkeys.
func (h *AuthHandler) confirmPassword(c *okapi.Context, user *models.User, password string) error {
	identifier := user.Email
	switch strings.ToLower(strings.TrimSpace(user.AuthSource)) {
	case "", models.AuthSourceLocal:
	case models.AuthSourceLDAP:
		identifier = user.Username
	default:
		return errPasskeyNeedsPassword
	}
	u, _, err := h.credentialCheck(c.Request().Context(), identifier, password)
	if err != nil || u.ID != user.ID {
		return auth.ErrInvalidCredentials
	}
	return nil
}

// PasskeyLoginBegin starts a passwordless sign-in.
func (h *AuthHandler) PasskeyLoginBegin(c *okapi.Context) error {
	token, opts, err := h.passkeys.BeginLogin(c.Request().Context())
	if err != nil {
		return h.passkeyAbort(c, err, "failed to start passkey sign-in")
	}
	return ok(c, PasskeyBegin{Token: token, Options: opts})
}

// A verified passkey stands in for password and TOTP; account policies still apply.
func (h *AuthHandler) PasskeyLogin(c *okapi.Context, req *PasskeyLoginRequest) error {
	if len(req.Body.Credential) == 0 {
		return c.AbortBadRequest("credential is required")
	}
	uid, err := h.passkeys.FinishLogin(c.Request().Context(), req.Body.Token, req.Body.Credential)
	if errors.Is(err, passkey.ErrInvalid) || errors.Is(err, passkey.ErrCeremony) {
		return c.AbortUnauthorized(err.Error())
	}
	if err != nil {
		return h.passkeyAbort(c, err, "failed to verify passkey")
	}
	user, err := h.users.FindByID(uid)
	if err != nil || user.IsService() {
		return c.AbortUnauthorized(passkey.ErrInvalid.Error())
	}
	switch {
	case !user.Active:
		err = auth.ErrAccountDisabled
	case h.settings.Bool(settings.KeyRequireEmailVerification, false) && user.EmailVerifiedAt == nil && !user.IsAdmin():
		err = errEmailUnverified
	case h.enforceSSO != nil && h.enforceSSO(user):
		err = errSSORequired
	}
	if err != nil {
		return h.abortCredential(c, err)
	}
	if user.MustChangePassword {
		token, err := h.auth.CreateResetSession(c.Request().Context(), user.ID)
		if err != nil {
			return c.AbortInternalServerError("failed to start password reset", err)
		}
		h.audit.Record(audit.Entry{ActorID: &user.ID, Action: "user.password_change_required", TargetType: "user", IP: c.RealIP()})
		return ok(c, AuthResponse{MustChangePassword: true, ResetToken: token})
	}
	now := time.Now()
	user.LastLoginAt = &now
	_ = h.users.Update(user)
	h.audit.Record(audit.Entry{ActorID: &user.ID, Action: "user.login", TargetType: "user", IP: c.RealIP(), Metadata: map[string]any{"via": "passkey"}})
	return h.issue(c, user, 200)
}

// ListPasskeys returns the caller's passkeys.
func (h *AuthHandler) ListPasskeys(c *okapi.Context) error {
	rows, err := h.passkeyRepo.ListByUser(middlewares.UserID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to list passkeys", err)
	}
	if rows == nil {
		rows = []models.Passkey{}
	}
	return ok(c, rows)
}

// Password and TOTP are confirmed first: a stolen session alone must not plant a passkey.
func (h *AuthHandler) PasskeyRegisterBegin(c *okapi.Context, req *PasskeyRegisterBeginRequest) error {
	user, err := h.users.FindByID(middlewares.UserID(c))
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if err := h.confirmPassword(c, user, req.Body.Password); err != nil {
		if errors.Is(err, errPasskeyNeedsPassword) {
			return c.AbortForbidden(err.Error())
		}
		return c.AbortBadRequest("password is incorrect")
	}
	if user.TwoFactorEnabled {
		if strings.TrimSpace(req.Body.TwoFactorCode) == "" {
			return c.AbortBadRequest("a two-factor code is required")
		}
		if !h.auth.VerifyLoginCode(user, strings.TrimSpace(req.Body.TwoFactorCode)) {
			return c.AbortBadRequest("invalid two-factor code")
		}
	}
	token, opts, err := h.passkeys.BeginRegistration(c.Request().Context(), user)
	if err != nil {
		return h.passkeyAbort(c, err, "failed to start passkey registration")
	}
	return ok(c, PasskeyBegin{Token: token, Options: opts})
}

// PasskeyRegister stores the passkey the browser created.
func (h *AuthHandler) PasskeyRegister(c *okapi.Context, req *PasskeyRegisterRequest) error {
	user, err := h.users.FindByID(middlewares.UserID(c))
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if len(req.Body.Credential) == 0 {
		return c.AbortBadRequest("credential is required")
	}
	p, err := h.passkeys.FinishRegistration(c.Request().Context(), user, req.Body.Token, req.Body.Name, req.Body.Credential)
	if err != nil {
		return h.passkeyAbort(c, err, "failed to save passkey")
	}
	h.audit.Record(audit.Entry{ActorID: &user.ID, Action: "user.passkey_added", TargetType: "passkey", TargetID: strconv.Itoa(int(p.ID)),
		IP: c.RealIP(), Metadata: map[string]any{"name": p.Name, "synced": p.Synced}})
	return created(c, p)
}

// RenamePasskey relabels one of the caller's passkeys.
func (h *AuthHandler) RenamePasskey(c *okapi.Context, req *PasskeyRenameRequest) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return c.AbortBadRequest("invalid passkey id")
	}
	uid := middlewares.UserID(c)
	found, err := h.passkeyRepo.Rename(uint(id), uid, passkey.Name(req.Body.Name))
	if err != nil {
		return c.AbortInternalServerError("failed to rename passkey", err)
	}
	if !found {
		return c.AbortNotFound("passkey not found")
	}
	h.audit.Record(audit.Entry{ActorID: &uid, Action: "user.passkey_renamed", TargetType: "passkey", TargetID: strconv.Itoa(id), IP: c.RealIP()})
	return message(c, "passkey renamed")
}

// DeletePasskey removes one of the caller's passkeys after re-confirming the password.
func (h *AuthHandler) DeletePasskey(c *okapi.Context, req *PasskeyRemoveRequest) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return c.AbortBadRequest("invalid passkey id")
	}
	user, err := h.users.FindByID(middlewares.UserID(c))
	if err != nil {
		return c.AbortNotFound("user not found")
	}
	if err := h.confirmPassword(c, user, req.Body.Password); err != nil {
		if errors.Is(err, errPasskeyNeedsPassword) {
			return c.AbortForbidden(err.Error())
		}
		return c.AbortBadRequest("password is incorrect")
	}
	found, err := h.passkeyRepo.Delete(uint(id), user.ID)
	if err != nil {
		return c.AbortInternalServerError("failed to remove passkey", err)
	}
	if !found {
		return c.AbortNotFound("passkey not found")
	}
	h.audit.Record(audit.Entry{ActorID: &user.ID, Action: "user.passkey_removed", TargetType: "passkey", TargetID: strconv.Itoa(id), IP: c.RealIP()})
	return message(c, "passkey removed")
}
