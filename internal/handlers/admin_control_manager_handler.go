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

// A standby control plane never sweeps, so it reports no sweep.
func (h *AdminControlManagerHandler) Status(c *okapi.Context) error {
	return ok(c, h.svc.Status())
}
