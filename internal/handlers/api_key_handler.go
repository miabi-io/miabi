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
	"strconv"
	"time"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/auth"
	"github.com/miabi-io/miabi/internal/services/elevation"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

type APIKeyHandler struct {
	keys       *auth.APIKeyService
	repo       *repositories.APIKeyRepository
	workspaces *repositories.WorkspaceRepository
	users      *repositories.UserRepository
	gate       *elevation.Service
	audit      *audit.Logger
}

// A fresh console unlock is required for admin-scope keys, which would otherwise bypass the unlock.
func (h *APIKeyHandler) SetElevation(users *repositories.UserRepository, gate *elevation.Service) {
	h.users, h.gate = users, gate
}

func NewAPIKeyHandler(keys *auth.APIKeyService, repo *repositories.APIKeyRepository, workspaces *repositories.WorkspaceRepository, auditLog *audit.Logger) *APIKeyHandler {
	return &APIKeyHandler{keys: keys, repo: repo, workspaces: workspaces, audit: auditLog}
}

type CreateAPIKeyRequest struct {
	Body struct {
		Name          string   `json:"name" required:"true"`
		Scopes        []string `json:"scopes" enum:"read,write,deploy,admin,*,registry_read,registry_write" default:"read"`
		AllowedIPs    []string `json:"allowed_ips"`
		ExpiresInDays *int     `json:"expires_in_days" min:"0"`
		WorkspaceID   *uint    `json:"workspace_id"`
	} `json:"body"`
}

type APIKeyCreated struct {
	ID          uint       `json:"id"`
	Name        string     `json:"name"`
	Key         string     `json:"key"` // shown once
	KeyPrefix   string     `json:"key_prefix"`
	Scopes      []string   `json:"scopes"`
	AllowedIPs  []string   `json:"allowed_ips"`
	WorkspaceID *uint      `json:"workspace_id,omitempty"` // set for a workspace-scoped key
	ExpiresAt   *time.Time `json:"expires_at"`
	Message     string     `json:"message"`
}

// Create issues a new API key for the authenticated user.
func (h *APIKeyHandler) Create(c *okapi.Context, req *CreateAPIKeyRequest) error {
	userID := middlewares.UserID(c)

	// Account-wide keys are allowed and reach only the user's own workspaces at request time.
	if req.Body.WorkspaceID != nil {
		if _, err := h.workspaces.FindMember(*req.Body.WorkspaceID, userID); err != nil {
			return c.AbortForbidden("you are not a member of this workspace")
		}
	}

	scopes, err := auth.NormalizeScopes(req.Body.Scopes)
	if err != nil {
		return c.AbortBadRequest("invalid scope", err)
	}
	if h.gate != nil && h.users != nil && req.Body.WorkspaceID == nil && grantsAdmin(scopes) {
		if u, uerr := h.users.FindByID(userID); uerr == nil && u.IsAdmin() {
			if ferr := middlewares.FreshUnlock(c, h.gate); ferr != nil {
				return c.AbortForbidden(ferr.Error(), ferr)
			}
		}
	}

	var expiresAt *time.Time
	if req.Body.ExpiresInDays != nil && *req.Body.ExpiresInDays > 0 {
		t := time.Now().AddDate(0, 0, *req.Body.ExpiresInDays)
		expiresAt = &t
	}

	plaintext, key, err := h.keys.Create(userID, req.Body.WorkspaceID, req.Body.Name, req.Body.AllowedIPs, scopes, expiresAt)
	if err != nil {
		if a := quotaAbort(c, err); a != nil {
			return a
		}
		return c.AbortInternalServerError("failed to create API key", err)
	}
	h.audit.Record(audit.Entry{ActorID: &userID, WorkspaceID: req.Body.WorkspaceID, Action: "api_key.create", TargetType: "api_key", TargetID: strconv.Itoa(int(key.ID)), IP: c.RealIP()})
	return created(c, APIKeyCreated{
		ID:          key.ID,
		Name:        key.Name,
		Key:         plaintext,
		KeyPrefix:   key.KeyPrefix,
		Scopes:      key.Scopes,
		AllowedIPs:  key.AllowedIPs,
		WorkspaceID: key.WorkspaceID,
		ExpiresAt:   key.ExpiresAt,
		Message:     "Save this key securely. It will not be shown again.",
	})
}

func grantsAdmin(scopes []string) bool {
	for _, s := range scopes {
		if s == models.ScopeAdmin || s == models.ScopeAll {
			return true
		}
	}
	return false
}

// List returns the authenticated user's API keys (without secrets).
func (h *APIKeyHandler) List(c *okapi.Context) error {
	keys, err := h.repo.ListByUser(middlewares.UserID(c))
	if err != nil {
		return c.AbortInternalServerError("failed to list API keys", err)
	}
	return ok(c, keys)
}

// Get returns a single API key owned by the authenticated user.
func (h *APIKeyHandler) Get(c *okapi.Context) error {
	key, err := h.ownedKey(c)
	if err != nil {
		return err
	}
	return ok(c, key)
}

// Revoke disables an API key owned by the authenticated user.
func (h *APIKeyHandler) Revoke(c *okapi.Context) error {
	key, err := h.ownedKey(c)
	if err != nil {
		return err
	}
	if err := h.repo.Revoke(key.ID); err != nil {
		return c.AbortInternalServerError("failed to revoke API key", err)
	}
	userID := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &userID, WorkspaceID: key.WorkspaceID, Action: "api_key.revoke", TargetType: "api_key", TargetID: strconv.Itoa(int(key.ID)), IP: c.RealIP()})
	return message(c, "API key revoked")
}

// Only inactive (revoked or expired) keys may be deleted.
func (h *APIKeyHandler) Delete(c *okapi.Context) error {
	key, err := h.ownedKey(c)
	if err != nil {
		return err
	}
	if key.IsValid() {
		return c.AbortBadRequest("active API keys cannot be deleted — revoke it first")
	}
	if err := h.repo.Delete(key.ID); err != nil {
		return c.AbortInternalServerError("failed to delete API key", err)
	}
	userID := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &userID, WorkspaceID: key.WorkspaceID, Action: "api_key.delete", TargetType: "api_key", TargetID: strconv.Itoa(int(key.ID)), IP: c.RealIP()})
	return message(c, "API key deleted")
}

func (h *APIKeyHandler) ownedKey(c *okapi.Context) (*models.APIKey, error) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return nil, c.AbortBadRequest("invalid key id")
	}
	key, err := h.repo.FindByID(uint(id))
	if err != nil || key.UserID != middlewares.UserID(c) {
		return nil, c.AbortNotFound("API key not found")
	}
	return key, nil
}
