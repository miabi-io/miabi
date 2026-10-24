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

package middlewares

import (
	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/models"
)

// BillingPathPrefix is the Billing API tree; billing keys are refused anywhere else.
const BillingPathPrefix = "/api/v1/billing/"

// RequireBillingCaller admits only a billing key: purpose billing, account-wide (no workspace,
// application or ephemeral binding). A session or any other key, "*" included, is refused.
func RequireBillingCaller() okapi.Middleware {
	return func(c *okapi.Context) error {
		if AuthMethod(c) != "api_key" || c.GetString(CtxAPIKeyPurpose) != models.KeyPurposeBilling {
			return c.AbortForbidden("the Billing API needs a billing key")
		}
		if APIKeyWorkspaceID(c) != nil || APIKeyAppID(c) != 0 || APIKeyEphemeral(c) {
			return c.AbortForbidden("a billing key must not be bound to a workspace or an application")
		}
		return c.Next()
	}
}

// RequireLicensed refuses unless the licence grants flag: 402 LICENSE_REQUIRED without one,
// 403 ENTITLEMENT_DENIED when it lacks the flag. It runs on every request, so installing or
// removing a licence applies without a restart.
func RequireLicensed(ee enterprise.EE, flag string) okapi.Middleware {
	return func(c *okapi.Context) error {
		var err error = enterprise.ErrLicenseRequired
		if ee != nil {
			err = ee.Require(flag)
		}
		if err == nil {
			return c.Next()
		}
		return AbortLicence(c, err)
	}
}

// AbortLicence answers a licence refusal with its own status (402 or 403).
func AbortLicence(c *okapi.Context, err error) error {
	status := 402
	if s, ok := err.(interface{ Status() int }); ok {
		status = s.Status()
	}
	return c.AbortWithError(status, err)
}
