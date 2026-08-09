// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"errors"
	"sort"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/config"
)

// ConfigHandler exposes workspace configuration files. A sensitive config's
// content is returned only through the explicit, admin-only, audited reveal
// endpoint; the list and detail views carry keys and digests but no content.
type ConfigHandler struct {
	svc   *config.Service
	audit *audit.Logger
}

func NewConfigHandler(svc *config.Service, auditLog *audit.Logger) *ConfigHandler {
	return &ConfigHandler{svc: svc, audit: auditLog}
}

type CreateConfigRequest struct {
	Body struct {
		Name        string            `json:"name" required:"true"`
		DisplayName string            `json:"display_name"`
		Description string            `json:"description"`
		Data        map[string]string `json:"data" required:"true"`
		Mode        string            `json:"mode"`
		Sensitive   bool              `json:"sensitive"`
		Delimiters  []string          `json:"delimiters"`
	} `json:"body"`
}

type UpdateConfigRequest struct {
	Body struct {
		// Data is optional: omitting it keeps the stored files (a metadata-only
		// edit); sending it replaces them wholesale.
		Data        map[string]string `json:"data"`
		DisplayName string            `json:"display_name"`
		Description string            `json:"description"`
		Mode        string            `json:"mode"`
		Delimiters  []string          `json:"delimiters"`
	} `json:"body"`
}

// configView is the safe projection of a config: never content, always the file
// keys and their sizes so the UI can render without a reveal.
type configView struct {
	*models.Config
	Keys  []string       `json:"keys"`
	Sizes map[string]int `json:"sizes"`
}

func (h *ConfigHandler) view(cfg *models.Config) (*configView, error) {
	data, err := h.svc.Data(cfg)
	if err != nil {
		return nil, err
	}
	v := &configView{Config: cfg, Keys: make([]string, 0, len(data)), Sizes: map[string]int{}}
	for k, content := range data {
		v.Keys = append(v.Keys, k)
		v.Sizes[k] = len(content)
	}
	sort.Strings(v.Keys)
	return v, nil
}

func (h *ConfigHandler) List(c *okapi.Context) error {
	page, size, offset := normalizePageParams(queryInt(c, "page", 0), queryInt(c, "size", 20))
	configs, total, err := h.svc.ListPaged(middlewares.WorkspaceID(c), c.Query("search"), queryBool(c, "managed"), size, offset)
	if err != nil {
		return c.AbortInternalServerError("failed to list configs", err)
	}
	views := make([]*configView, 0, len(configs))
	for i := range configs {
		v, verr := h.view(&configs[i])
		if verr != nil {
			return c.AbortInternalServerError("failed to read config", verr)
		}
		views = append(views, v)
	}
	return paginated(c, views, total, page, size)
}

func (h *ConfigHandler) Get(c *okapi.Context) error {
	cfg, err := h.svc.Get(middlewares.WorkspaceID(c), h.id(c))
	if err != nil {
		return h.mapErr(c, err)
	}
	v, err := h.view(cfg)
	if err != nil {
		return c.AbortInternalServerError("failed to read config", err)
	}
	return ok(c, v)
}

func (h *ConfigHandler) Create(c *okapi.Context, req *CreateConfigRequest) error {
	wsID := middlewares.WorkspaceID(c)
	actor := middlewares.UserID(c)
	cfg, err := h.svc.Create(wsID, config.Input{
		Name: req.Body.Name, DisplayName: req.Body.DisplayName, Description: req.Body.Description,
		Data: req.Body.Data, Mode: req.Body.Mode, Sensitive: req.Body.Sensitive, Delimiters: req.Body.Delimiters,
	}, &actor)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "config.create", cfg.ID)
	v, err := h.view(cfg)
	if err != nil {
		return c.AbortInternalServerError("failed to read config", err)
	}
	return created(c, v)
}

func (h *ConfigHandler) Update(c *okapi.Context, req *UpdateConfigRequest) error {
	wsID := middlewares.WorkspaceID(c)
	actor := middlewares.UserID(c)
	cfg, err := h.svc.Update(wsID, h.id(c), config.Input{
		Data: req.Body.Data, DisplayName: req.Body.DisplayName, Description: req.Body.Description,
		Mode: req.Body.Mode, Delimiters: req.Body.Delimiters,
	}, &actor)
	if err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "config.update", cfg.ID)
	v, err := h.view(cfg)
	if err != nil {
		return c.AbortInternalServerError("failed to read config", err)
	}
	return ok(c, v)
}

// Reveal returns a config's decrypted files. Audited, and gated on app:admin the
// same way a secret reveal is.
func (h *ConfigHandler) Reveal(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	cfg, err := h.svc.Get(wsID, h.id(c))
	if err != nil {
		return h.mapErr(c, err)
	}
	data, err := h.svc.Data(cfg)
	if err != nil {
		return c.AbortInternalServerError("failed to decrypt config", err)
	}
	h.record(c, wsID, "config.reveal", cfg.ID)
	return ok(c, map[string]any{"data": data})
}

func (h *ConfigHandler) Usage(c *okapi.Context) error {
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

func (h *ConfigHandler) Delete(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	if err := h.svc.Delete(wsID, h.id(c)); err != nil {
		return h.mapErr(c, err)
	}
	h.record(c, wsID, "config.delete", h.id(c))
	return message(c, "config deleted")
}

func (h *ConfigHandler) id(c *okapi.Context) uint {
	id, _ := resolveID(c.Param("configID"), h.svc.IDByUID)
	return id
}

func (h *ConfigHandler) mapErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, config.ErrNotFound):
		return c.AbortNotFound("config not found")
	case errors.Is(err, config.ErrNameTaken):
		return c.AbortWithError(409, err)
	case errors.Is(err, config.ErrInvalidName):
		return c.AbortBadRequest("invalid config name (lowercase letters, digits and - only)")
	case errors.Is(err, config.ErrNoData):
		return c.AbortBadRequest("at least one file is required")
	case errors.Is(err, config.ErrKeyNotFound):
		return c.AbortNotFound("config has no such file")
	case errors.Is(err, config.ErrInUse):
		return c.AbortWithError(409, err)
	default:
		return c.AbortInternalServerError("config operation failed", err)
	}
}

func (h *ConfigHandler) record(c *okapi.Context, wsID uint, action string, id uint) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: action, TargetType: "config", TargetID: strconv.Itoa(int(id)), IP: c.RealIP()})
}
