// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// CapabilityHandler serves the grantable catalogue and the platform-wide inventory.
type CapabilityHandler struct {
	apps       *repositories.ApplicationRepository
	workspaces *repositories.WorkspaceRepository
	enabled    bool
}

func NewCapabilityHandler(enabled bool, apps *repositories.ApplicationRepository, workspaces *repositories.WorkspaceRepository) *CapabilityHandler {
	return &CapabilityHandler{enabled: enabled, apps: apps, workspaces: workspaces}
}

// CapabilityCatalog is what a workspace may be offered.
type CapabilityCatalog struct {
	// Enabled false means the lists are empty because nothing may be granted.
	Enabled         bool                   `json:"enabled"`
	Capabilities    []models.Capability    `json:"capabilities"`
	Devices         []models.DeviceCatalog `json:"devices"`
	MaxCapabilities int                    `json:"max_capabilities"`
	MaxDevices      int                    `json:"max_devices"`
}

// Catalog lists every grantable capability and device with its tier, so the
// console renders a picker rather than a text box.
func (h *CapabilityHandler) Catalog(c *okapi.Context) error {
	if !h.enabled {
		// Empty slices, not nil, so the console need not tell absent from empty.
		return ok(c, CapabilityCatalog{
			Capabilities: []models.Capability{},
			Devices:      []models.DeviceCatalog{},
		})
	}
	return ok(c, CapabilityCatalog{
		Enabled:         true,
		Capabilities:    models.Capabilities(),
		Devices:         models.Devices(),
		MaxCapabilities: models.MaxCapabilities,
		MaxDevices:      models.MaxDevices,
	})
}

// GrantedApp is one application holding a capability or device grant.
type GrantedApp struct {
	ApplicationID   uint     `json:"application_id"`
	ApplicationName string   `json:"application_name"`
	WorkspaceID     uint     `json:"workspace_id"`
	WorkspaceName   string   `json:"workspace_name"`
	Elevated        bool     `json:"elevated"`
	AddCapabilities []string `json:"add_capabilities,omitempty"`
	Devices         []string `json:"devices,omitempty"`
}

// Inventory lists every application on the platform holding a grant. A grant is
// otherwise visible only inside the app that holds it.
func (h *CapabilityHandler) Inventory(c *okapi.Context) error {
	apps, err := h.apps.ListWithGrants()
	if err != nil {
		return c.AbortInternalServerError("failed to list granted applications", err)
	}
	names := map[uint]string{}
	out := make([]GrantedApp, 0, len(apps))
	for i := range apps {
		a := apps[i]
		name, cached := names[a.WorkspaceID]
		if !cached {
			if ws, werr := h.workspaces.FindByID(a.WorkspaceID); werr == nil {
				name = ws.DisplayName
				if name == "" {
					name = ws.Name
				}
			}
			names[a.WorkspaceID] = name
		}
		tier := models.HighestCapabilityTier(a.AddCapabilities)
		if dt := models.HighestDeviceTier(a.Devices); dt > tier {
			tier = dt
		}
		out = append(out, GrantedApp{
			ApplicationID:   a.ID,
			ApplicationName: a.DisplayName,
			WorkspaceID:     a.WorkspaceID,
			WorkspaceName:   name,
			Elevated:        tier >= models.TierElevated,
			AddCapabilities: a.AddCapabilities,
			Devices:         a.Devices,
		})
	}
	return ok(c, out)
}
