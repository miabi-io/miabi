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
	"errors"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/eventbus"
	"github.com/miabi-io/miabi/internal/services/marketplace"
)

type MarketplaceHandler struct {
	svc    *marketplace.Service
	audit  *audit.Logger
	placer *Placer
}

func NewMarketplaceHandler(svc *marketplace.Service, auditLog *audit.Logger) *MarketplaceHandler {
	return &MarketplaceHandler{svc: svc, audit: auditLog}
}

// SetPlacer wires the platform-admin check that lets an install target a restricted location.
func (h *MarketplaceHandler) SetPlacer(p *Placer) { h.placer = p }

// Placements pins a database dependency (by manifest db name) to an existing instance id.
type InstallRequest struct {
	Body struct {
		Name           string            `json:"name" required:"true"` // template handle
		Version        string            `json:"version"`
		DisplayName    string            `json:"display_name"` // optional display-name override
		Inputs         map[string]string `json:"inputs"`
		Placements     map[string]uint   `json:"placements"`
		PlacementModes map[string]string `json:"placement_modes"`
		// Location is where the install runs; empty uses the workspace's default location.
		Location string `json:"location"`
	} `json:"body"`
}

func (h *MarketplaceHandler) ListTemplates(c *okapi.Context) error {
	return ok(c, h.svc.ListPublic())
}

func (h *MarketplaceHandler) GetTemplate(c *okapi.Context) error {
	name := c.Param("name")
	entry, found := h.svc.GetPublicEntry(name)
	if !found {
		return c.AbortNotFound("template not found")
	}
	m, _ := h.svc.GetPublicManifest(name, c.Query("version"))
	return ok(c, map[string]any{"entry": entry, "manifest": m})
}

// Install instantiates a template in the workspace (synchronous).
func (h *MarketplaceHandler) Install(c *okapi.Context, req *InstallRequest) error {
	wsID := middlewares.WorkspaceID(c)
	result, err := h.svc.Install(c.Request().Context(), wsID, marketplace.InstallInput{
		Name:           req.Body.Name,
		Version:        req.Body.Version,
		DisplayName:    req.Body.DisplayName,
		Inputs:         req.Body.Inputs,
		Placements:     req.Body.Placements,
		PlacementModes: req.Body.PlacementModes,
		Location:       req.Body.Location,
		Admin:          h.placer.admin(c),
	})
	if err != nil {
		switch {
		case errors.Is(err, marketplace.ErrTemplateNotFound):
			return c.AbortNotFound("template not found")
		case errors.Is(err, marketplace.ErrMissingInput),
			errors.Is(err, marketplace.ErrInvalidInput),
			errors.Is(err, marketplace.ErrNoSharedInstance):
			return c.AbortBadRequest(err.Error())
		default:
			if a := placementAbort(c, err); a != nil {
				return a
			}
			return c.AbortInternalServerError("install failed", err)
		}
	}
	actor := middlewares.UserID(c)
	target := req.Body.Name
	if len(result.Apps) > 0 {
		target = strconv.Itoa(int(result.Apps[0].ID))
	}
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: "marketplace.install", TargetType: "template:" + req.Body.Name, TargetID: target, IP: c.RealIP()})
	return created(c, result)
}

func (h *MarketplaceHandler) StartInstall(c *okapi.Context, req *InstallRequest) error {
	wsID := middlewares.WorkspaceID(c)
	job, err := h.svc.StartInstall(wsID, marketplace.InstallInput{
		Name:           req.Body.Name,
		Version:        req.Body.Version,
		DisplayName:    req.Body.DisplayName,
		Inputs:         req.Body.Inputs,
		Placements:     req.Body.Placements,
		PlacementModes: req.Body.PlacementModes,
		Location:       req.Body.Location,
		Admin:          h.placer.admin(c),
	})
	if err != nil {
		switch {
		case errors.Is(err, marketplace.ErrTemplateNotFound):
			return c.AbortNotFound("template not found")
		case errors.Is(err, marketplace.ErrMissingInput),
			errors.Is(err, marketplace.ErrInvalidInput),
			errors.Is(err, marketplace.ErrNoSharedInstance):
			return c.AbortBadRequest(err.Error())
		default:
			if a := placementAbort(c, err); a != nil {
				return a
			}
			return c.AbortInternalServerError("install failed", err)
		}
	}
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: "marketplace.install", TargetType: "template:" + req.Body.Name, TargetID: req.Body.Name, IP: c.RealIP()})
	return created(c, job)
}

func (h *MarketplaceHandler) LocationDatabases(c *okapi.Context) error {
	list, err := h.svc.LocationDatabases(middlewares.WorkspaceID(c), c.Query("location"), h.placer.admin(c))
	if err != nil {
		if a := placementAbort(c, err); a != nil {
			return a
		}
		return c.AbortInternalServerError("failed to list databases", err)
	}
	return ok(c, list)
}

// InstallJob returns a one-shot snapshot of an install job (REST fallback).
func (h *MarketplaceHandler) InstallJob(c *okapi.Context) error {
	snap, found := h.svc.InstallJobSnapshot(c.Param("jobID"))
	if !found {
		return c.AbortNotFound("install job not found")
	}
	return ok(c, snap)
}

// InstallJobEvents streams an install job's live progress over SSE.
func (h *MarketplaceHandler) InstallJobEvents(c *okapi.Context) error {
	found, err := h.svc.StreamInstall(c.Request().Context(), c.Param("jobID"), func(e eventbus.Event) error {
		return c.SSESendJSON(e)
	})
	if !found {
		return c.AbortNotFound("install job not found")
	}
	return err
}

// ImportRequest imports a user-supplied template manifest into the workspace.
type ImportRequest struct {
	Body struct {
		YAML string `json:"yaml" required:"true"`
	} `json:"body"`
}

// ImportTemplate validates and stores a custom template in the workspace.
func (h *MarketplaceHandler) ImportTemplate(c *okapi.Context, req *ImportRequest) error {
	wsID := middlewares.WorkspaceID(c)
	entry, err := h.svc.Import(wsID, req.Body.YAML)
	if err != nil {
		if errors.Is(err, marketplace.ErrInvalidTemplate) {
			return c.AbortBadRequest(err.Error())
		}
		return c.AbortInternalServerError("import failed", err)
	}
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: "marketplace.import", TargetType: "template:" + entry.Name, TargetID: entry.Name, IP: c.RealIP()})
	return created(c, entry)
}

// UpdateTemplateRequest replaces a custom template's manifest in place.
type UpdateTemplateRequest struct {
	Body struct {
		YAML string `json:"yaml" required:"true"`
	} `json:"body"`
}

// Official templates are not editable, so are not exposed here.
func (h *MarketplaceHandler) GetCustomTemplateYAML(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	name := c.Param("name")
	raw, found := h.svc.GetCustomYAML(wsID, name, c.Query("version"))
	if !found {
		return c.AbortNotFound("custom template not found")
	}
	return ok(c, map[string]any{"name": name, "yaml": raw})
}

// UpdateTemplate validates and replaces a custom template's manifest in place.
func (h *MarketplaceHandler) UpdateTemplate(c *okapi.Context, req *UpdateTemplateRequest) error {
	wsID := middlewares.WorkspaceID(c)
	name := c.Param("name")
	entry, err := h.svc.UpdateCustom(wsID, name, req.Body.YAML)
	if err != nil {
		switch {
		case errors.Is(err, marketplace.ErrTemplateNotFound):
			return c.AbortNotFound("custom template not found")
		case errors.Is(err, marketplace.ErrInvalidTemplate):
			return c.AbortBadRequest(err.Error())
		default:
			return c.AbortInternalServerError("update failed", err)
		}
	}
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: "marketplace.update", TargetType: "template:" + name, TargetID: name, IP: c.RealIP()})
	return ok(c, entry)
}

// DeleteTemplate removes a custom template (all versions) from the workspace.
func (h *MarketplaceHandler) DeleteTemplate(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	name := c.Param("name")
	if err := h.svc.DeleteCustom(wsID, name); err != nil {
		if errors.Is(err, marketplace.ErrTemplateNotFound) {
			return c.AbortNotFound("custom template not found")
		}
		return c.AbortInternalServerError("delete failed", err)
	}
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: "marketplace.delete", TargetType: "template:" + name, TargetID: name, IP: c.RealIP()})
	return ok(c, map[string]any{"deleted": true})
}

func (h *MarketplaceHandler) ListWorkspaceTemplates(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	list, err := h.svc.ListForWorkspace(wsID)
	if err != nil {
		return c.AbortInternalServerError("failed to list templates", err)
	}
	return ok(c, list)
}

// Custom takes precedence over official.
func (h *MarketplaceHandler) GetWorkspaceTemplate(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	name := c.Param("name")
	entry, found := h.svc.GetEntryForWorkspace(wsID, name)
	if !found {
		return c.AbortNotFound("template not found")
	}
	m, _ := h.svc.GetManifestForWorkspace(wsID, name, c.Query("version"))
	return ok(c, map[string]any{"entry": entry, "manifest": m})
}

func (h *MarketplaceHandler) ListInstalls(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	installs, err := h.svc.ListInstalls(wsID)
	if err != nil {
		return c.AbortInternalServerError("failed to list installs", err)
	}
	return ok(c, installs)
}

func (h *MarketplaceHandler) UpgradePlan(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	id, err := strconv.Atoi(c.Param("installID"))
	if err != nil {
		return c.AbortBadRequest("invalid install id")
	}
	plan, err := h.svc.PlanUpgrade(wsID, uint(id), c.Query("version"))
	if err != nil {
		return h.mapUpgradeErr(c, err)
	}
	return ok(c, plan)
}

// UpgradeRequest is the optional body for applying an upgrade.
type UpgradeRequest struct {
	Body struct {
		Version string            `json:"version"` // target version ("" = latest)
		Inputs  map[string]string `json:"inputs"`  // answers for inputs new in this version
	} `json:"body"`
}

// Items that need manual review come back as warnings in the result.
func (h *MarketplaceHandler) Upgrade(c *okapi.Context, req *UpgradeRequest) error {
	wsID := middlewares.WorkspaceID(c)
	id, err := strconv.Atoi(c.Param("installID"))
	if err != nil {
		return c.AbortBadRequest("invalid install id")
	}
	res, err := h.svc.ApplyUpgrade(c.Request().Context(), wsID, uint(id), req.Body.Version, req.Body.Inputs)
	if err != nil {
		return h.mapUpgradeErr(c, err)
	}
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: "marketplace.upgrade", TargetType: "install", TargetID: c.Param("installID"), IP: c.RealIP()})
	return ok(c, res)
}

func (h *MarketplaceHandler) mapUpgradeErr(c *okapi.Context, err error) error {
	switch {
	case errors.Is(err, marketplace.ErrInstallNotFound), errors.Is(err, marketplace.ErrTemplateNotFound):
		return c.AbortNotFound(err.Error())
	case errors.Is(err, marketplace.ErrAlreadyLatest), errors.Is(err, marketplace.ErrStructuralChange):
		return c.AbortBadRequest(err.Error())
	default:
		return c.AbortInternalServerError("upgrade failed", err)
	}
}

// Uninstall tears down everything an install created and removes the record.
func (h *MarketplaceHandler) Uninstall(c *okapi.Context) error {
	wsID := middlewares.WorkspaceID(c)
	id, err := strconv.Atoi(c.Param("installID"))
	if err != nil {
		return c.AbortBadRequest("invalid install id")
	}
	result, err := h.svc.Uninstall(c.Request().Context(), wsID, uint(id))
	if err != nil {
		if errors.Is(err, marketplace.ErrInstallNotFound) {
			return c.AbortNotFound(err.Error())
		}
		return c.AbortInternalServerError("uninstall failed", err)
	}
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{ActorID: &actor, WorkspaceID: &wsID, Action: "marketplace.uninstall", TargetType: "install", TargetID: c.Param("installID"), IP: c.RealIP()})
	return ok(c, result)
}
