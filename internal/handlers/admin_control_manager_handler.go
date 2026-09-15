// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/services/controlmanager"
)

// AdminControlManagerHandler reports what the control manager found.
type AdminControlManagerHandler struct {
	svc *controlmanager.Service
}

func NewAdminControlManagerHandler(svc *controlmanager.Service) *AdminControlManagerHandler {
	return &AdminControlManagerHandler{svc: svc}
}

// Status returns the last sweep: the mode, the workloads found missing, and the nodes and clusters that could
// not be observed. A standby control plane never sweeps, so it reports no sweep.
func (h *AdminControlManagerHandler) Status(c *okapi.Context) error {
	return ok(c, h.svc.Status())
}
