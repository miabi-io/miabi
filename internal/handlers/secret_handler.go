// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/secret"
)

// SecretHandler exposes the workspace Vault. Values are write-only over the API:
// they are accepted on create/update and returned only via the explicit,
// admin-only, audited reveal endpoint.
type SecretHandler struct {
	svc   *secret.Service
	audit *audit.Logger
}

func NewSecretHandler(svc *secret.Service, auditLog *audit.Logger) *SecretHandler {
	return &SecretHandler{svc: svc, audit: auditLog}
}

type CreateSecretRequest struct {
	Body struct {
		Name        string `json:"name" required:"true"`
		Value       string `json:"value" required:"true"`
		Description string `json:"description"`
	} `json:"body"`
}

type UpdateSecretRequest struct {
	Body struct {
		// Value is optional on update: blank keeps the stored value (description-
		// only edit); a new value rotates the secret.
		Value       string `json:"value"`
		Description string `json:"description"`
	} `json:"body"`
}

// List returns a paginated, searchable list of a workspace's secrets (no
// values). ?managed=true|false narrows to platform-owned or hand-created
// secrets; omitting it returns both.
func (h *SecretHandler) List(c *okapi.Context) error {
	page, size, offset := normalizePageParams(queryInt(c, "page", 0), queryInt(c, "size", 20))
	secrets, total, err := h.svc.ListPaged(middlewares.WorkspaceID(c), c.Query("search"), queryBool(c, "managed"), size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list secrets", err)
	}
	return paginated(c, secrets, total, page, size)
}

func (h *SecretHandler) Create(c *okapi.Context, req *CreateSecretRequest) error {
	wsID := middlewares.WorkspaceID(c)
	actor := middlewares.UserID(c)
	sec, err := h.svc.Create(wsID, req.Body.Name, req.Body.Value, req.Body.Description, &actor)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "secret.create", sec.ID)
	return created(c, sec)
}

func (h *SecretHandler) Update(c *okapi.Context, req *UpdateSecretRequest) error {
	wsID := middlewares.WorkspaceID(c)
	actor := middlewares.UserID(c)
	sec, err := h.svc.Update(wsID, h.id(c), req.Body.Value, req.Body.Description, &actor)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "secret.update", sec.ID)
	return ok(c, sec)
}

// Reveal returns a secret's decrypted value (admin only, audited).
func (h *SecretHandler) Reveal(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	val, err := h.svc.Reveal(wsID, h.id(c))
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "secret.reveal", h.id(c))
	return ok(c, map[string]string{"value": val})
}

// Usage lists the apps that reference a secret.
func (h *SecretHandler) Usage(c *okapi.Context) error {
	apps, err := h.svc.Usage(middlewares.WorkspaceID(c), h.id(c))
	if err != nil {
		return h.mapErr(c, err)
	}
	out := make([]map[string]any, 0, len(apps))
	for i := range apps {
		out = append(out, map[string]any{"id": apps[i].ID, "name": apps[i].Name})
	}
	return ok(c, out)
}

func (h *SecretHandler) Delete(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	if err := h.svc.Delete(wsID, h.id(c)); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "secret.delete", h.id(c))
	return message(c, "secret deleted")
}

func (h *SecretHandler) id(c *okapi.Context) uint {
	id, _ := resolveID(c.Param("secretID"), h.svc.IDByUID)
	return id
}

func (h *SecretHandler) mapErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, secret.ErrNotFound):
		return c.AbortNotFound("secret not found")
	case errors.Is(err, secret.ErrNameTaken):
		return c.AbortWithError(409, err)
	case errors.Is(err, secret.ErrInvalidName):
		return c.AbortBadRequest("invalid secret name (use letters, digits, _ or -)")
	case errors.Is(err, secret.ErrNoValue):
		return c.AbortBadRequest("a value is required")
	case errors.Is(err, secret.ErrInUse):
		return c.AbortWithError(409, err)
	case errors.Is(err, secret.ErrManaged):
		return c.AbortWithError(409, err)
	default:
		return c.AbortInternalServerError("secret operation failed", err)
	}
}

func (h *SecretHandler) record(c *okapi.Context, wsID uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action, TargetType: "secret", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}
