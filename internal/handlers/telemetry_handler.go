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
	"github.com/miabi-io/miabi/internal/services/telemetry"
)

// TelemetryHandler shows admins exactly what the anonymous telemetry event contains.
type TelemetryHandler struct {
	svc *telemetry.Service
}

func NewTelemetryHandler(svc *telemetry.Service) *TelemetryHandler {
	return &TelemetryHandler{svc: svc}
}

// TelemetryPreview is the event as it would be sent, and whether it is sent at all.
type TelemetryPreview struct {
	Enabled bool              `json:"enabled"`
	URL     string            `json:"url"`
	Payload telemetry.Payload `json:"payload"`
}

func (h *TelemetryHandler) Preview(c *okapi.Context) error {
	return ok(c, TelemetryPreview{
		Enabled: h.svc.Enabled(),
		URL:     h.svc.URL(),
		Payload: h.svc.Payload(telemetry.EventDaily),
	})
}
