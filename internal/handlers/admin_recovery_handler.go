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
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/recovery"
)

// Not gated: a recovered platform may not reach its license server, and refusing would leave recovery stuck.
type AdminRecoveryHandler struct {
	svc   *recovery.Service
	audit *audit.Logger
}

func NewAdminRecoveryHandler(svc *recovery.Service, auditLog *audit.Logger) *AdminRecoveryHandler {
	return &AdminRecoveryHandler{svc: svc, audit: auditLog}
}

// CompleteRecoveryRequest carries the operator's explicit confirmation.
type CompleteRecoveryRequest struct {
	Body struct {
		// Completing recovery resumes schedules and cert issuance, which fail while DNS still points at the old host.
		Confirm bool `json:"confirm"`
	} `json:"body"`
}

// Status reports whether this platform is recovering, and the last report.
func (h *AdminRecoveryHandler) Status(c *okapi.Context) error {
	return ok(c, h.svc.Status())
}

// Reconcile converges restored state onto this host and returns the report.
func (h *AdminRecoveryHandler) Reconcile(c *okapi.Context) error {
	report, err := h.svc.Reconcile(c.Request().Context())
	if err != nil {
		return c.AbortInternalServerError("recovery reconcile failed", err)
	}
	h.record(c, "platform.recovery.reconcile")
	return ok(c, report)
}

// Complete clears the quiesce marker and resumes normal operation.
func (h *AdminRecoveryHandler) Complete(c *okapi.Context, req *CompleteRecoveryRequest) error {
	if !req.Body.Confirm {
		return c.AbortBadRequest("completing recovery resumes schedules and certificate issuance; it must be explicitly confirmed")
	}
	if err := h.svc.Complete(); err != nil {
		return c.AbortInternalServerError("failed to complete recovery", err)
	}
	h.record(c, "platform.recovery.complete")
	return message(c, "recovery completed; the platform is running normally")
}

func (h *AdminRecoveryHandler) record(c *okapi.Context, action string) {
	actor := middlewares.UserID(c)
	h.audit.Record(audit.Entry{
		ActorID:    &actor,
		Action:     action,
		TargetType: "platform_recovery",
		IP:         c.RealIP(),
	})
}
