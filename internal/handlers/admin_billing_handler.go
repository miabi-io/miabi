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
	"net/http"
	"strconv"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/middlewares"
	"github.com/miabi-io/miabi/internal/services/audit"
	"github.com/miabi-io/miabi/internal/services/billing"
)

// errBillingAPIDisabled answers the admin billing endpoints while MIABI_BILLING_API_ENABLED is off.
var errBillingAPIDisabled = billing.NewCodedError(http.StatusConflict, "BILLING_API_DISABLED",
	"the Billing API is off; set MIABI_BILLING_API_ENABLED=true and restart to use it")

// AdminBillingHandler is the admin Billing page: connect this Miabi to a billing console with an
// enrolment code, see the connection, and disconnect.
type AdminBillingHandler struct {
	svc     *billing.Service
	enabled bool
	ee      enterprise.EE
	audit   *audit.Logger
}

// NewAdminBillingHandler builds the handler.
func NewAdminBillingHandler(svc *billing.Service, enabled bool, ee enterprise.EE, a *audit.Logger) *AdminBillingHandler {
	return &AdminBillingHandler{svc: svc, enabled: enabled, ee: ee, audit: a}
}

// AdminBillingStatus is the Billing page: the API state and the console connection.
type AdminBillingStatus struct {
	BillingAPI string              `json:"billing_api" enum:"off,unlicensed,on"`
	Connection *billing.Connection `json:"connection"`
}

// ConnectBillingRequest connects this Miabi with a console enrolment code.
type ConnectBillingRequest struct {
	Body struct {
		Code string `json:"code" required:"true" doc:"The mce1.… enrolment code shown by the console"`
	} `json:"body"`
}

// usable writes the refusal and reports false while the switch is off or the licence lacks billing.
// AbortWithError returns nil once the response is written, so callers must check the bool, not an error.
func (h *AdminBillingHandler) usable(c *okapi.Context) (bool, error) {
	switch BillingAPIState(h.enabled, h.ee) {
	case BillingAPIOff:
		return false, c.AbortWithError(errBillingAPIDisabled.Status, errBillingAPIDisabled)
	case BillingAPIUnlicensed:
		var err error = enterprise.ErrLicenseRequired
		if h.ee != nil {
			err = h.ee.Require(enterprise.FlagBilling)
		}
		return false, middlewares.AbortLicence(c, err)
	}
	return true, nil
}

// Status reports the Billing API state and the console connection.
func (h *AdminBillingHandler) Status(c *okapi.Context) error {
	out := AdminBillingStatus{BillingAPI: BillingAPIState(h.enabled, h.ee), Connection: &billing.Connection{}}
	if out.BillingAPI != BillingAPIOff {
		conn, err := h.svc.Connection()
		if err != nil {
			return c.AbortInternalServerError("failed to read the billing connection", err)
		}
		out.Connection = conn
	}
	return ok(c, out)
}

// Connect enrols this Miabi with the console that issued the code.
func (h *AdminBillingHandler) Connect(c *okapi.Context, req *ConnectBillingRequest) error {
	if ok, err := h.usable(c); !ok {
		return err
	}
	actor := middlewares.UserID(c)
	conn, err := h.svc.Enroll(c.Request().Context(), req.Body.Code, actor)
	if err != nil {
		return billingErr(c, err, "failed to connect to the billing console")
	}
	h.audit.Record(audit.Entry{ActorID: &actor, Action: "admin.billing.connect", TargetType: "billing", TargetID: conn.ConsoleInstanceID, IP: c.RealIP(),
		Metadata: map[string]any{"console_url": conn.ConsoleURL, "key_prefix": conn.KeyPrefix}})
	return ok(c, conn)
}

// Disconnect revokes the billing key and forgets the console.
func (h *AdminBillingHandler) Disconnect(c *okapi.Context) error {
	if ok, err := h.usable(c); !ok {
		return err
	}
	before, _ := h.svc.Connection()
	actor := middlewares.UserID(c)
	if err := h.svc.Disconnect(actor); err != nil {
		if errors.Is(err, billing.ErrConnectionLocked) {
			h.audit.Record(audit.Entry{ActorID: &actor, Action: "admin.billing.disconnect_refused", TargetType: "billing", TargetID: strconv.Itoa(int(actor)), IP: c.RealIP()})
		}
		return billingErr(c, err, "failed to disconnect the billing console")
	}
	md := map[string]any{}
	if before != nil {
		md["console_url"], md["console_instance_id"] = before.ConsoleURL, before.ConsoleInstanceID
	}
	h.audit.Record(audit.Entry{ActorID: &actor, Action: "admin.billing.disconnect", TargetType: "billing", TargetID: strconv.Itoa(int(actor)), IP: c.RealIP(), Metadata: md})
	return message(c, "billing console disconnected")
}

func billingErr(c *okapi.Context, err error, fallback string) error {
	var ce *billing.CodedError
	if errors.As(err, &ce) {
		return c.AbortWithError(ce.Status, ce)
	}
	return c.AbortInternalServerError(fallback, err)
}
