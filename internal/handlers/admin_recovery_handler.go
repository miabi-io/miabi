// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/recovery"
)

// AdminRecoveryHandler drives the post-restore reconcile from the admin UI. Deliberately NOT
// gated on the Enterprise entitlement: a platform recovered onto fresh hardware may not have
// reached its license server, and refusing to finish would turn an outage into a stuck one.
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
		// Confirm asserts the operator has read the reconcile report and moved DNS. Completing recovery
		// resumes schedules and certificate issuance, so doing it while DNS still points at the old host
		// produces failures that look like the restore did not work.
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
