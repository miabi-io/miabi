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
	"net/http"

	"github.com/jkaninda/okapi"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/billing"
)

// SetMigration lets the Billing API mint migration keys and start imports for an account (v1.2).
func (h *BillingHandler) SetMigration(m *MigrationHandler) { h.migration = m }

// BillingMigrationStartRequest opens a migration of one of the account's workspaces.
type BillingMigrationStartRequest struct {
	ExternalID string `path:"external_id" required:"true"`
	Workspace  string `path:"workspace" required:"true" doc:"Workspace uid or handle"`
	Body       struct {
		ExpiresInHours int `json:"expires_in_hours" doc:"1 to 168; 24 when omitted"`
	} `json:"body"`
}

func (h *BillingHandler) MigrationStart(c *okapi.Context, req *BillingMigrationStartRequest) error {
	return h.write(c, req.Body, func() (any, error) {
		if h.migration == nil || !h.migration.enabled {
			return nil, billing.NewCodedError(http.StatusForbidden, errMigrationOff.code, errMigrationOff.msg)
		}
		owner, wsID, err := h.svc.MigrationAccount(req.ExternalID, req.Workspace)
		if err != nil {
			return nil, err
		}
		out, err := h.migration.startFor(owner, &wsID, req.Body.ExpiresInHours)
		if err != nil {
			return nil, billingCoded(err)
		}
		h.record(c, "billing.workspace.migration_start", req.ExternalID, map[string]any{"workspace": req.Workspace, "expires_at": out.ExpiresAt})
		return out, nil
	})
}

// BillingMigrationImportRequest pulls a workspace from another install into a new workspace billed to the account.
type BillingMigrationImportRequest struct {
	ExternalID string              `path:"external_id" required:"true"`
	Body       MigrationImportBody `json:"body"`
}

// BillingMigrationImport is a started import and the account it was attached to.
type BillingMigrationImport struct {
	Run     *models.WorkspaceBundle `json:"run"`
	Account *billing.Account        `json:"account"`
}

// Refused past max_workspaces before anything is created.
func (h *BillingHandler) MigrationImport(c *okapi.Context, req *BillingMigrationImportRequest) error {
	return h.write(c, req.Body, func() (any, error) {
		if h.migration == nil || !h.migration.enabled {
			return nil, billing.NewCodedError(http.StatusForbidden, errMigrationOff.code, errMigrationOff.msg)
		}
		owner, _, err := h.svc.MigrationAccount(req.ExternalID, "")
		if err != nil {
			return nil, err
		}
		ctx := c.Request().Context()
		run, err := h.migration.svc.StartMigrationImport(ctx, owner, req.Body.input(""))
		if err != nil {
			return nil, billingCoded(err)
		}
		ws, err := h.migration.workspaces.FindByID(run.WorkspaceID)
		if err != nil {
			return nil, err
		}
		acct, err := h.svc.AddWorkspace(ctx, req.ExternalID, billing.WorkspaceAttach{Workspace: ws.Name})
		if err != nil {
			return nil, err
		}
		h.record(c, "billing.workspace.migration_import", req.ExternalID, map[string]any{
			"source_url": req.Body.URL, "source_workspace": run.SourceWorkspace, "workspace": ws.Name,
		})
		return BillingMigrationImport{Run: run, Account: acct}, nil
	})
}

// BillingMigrationRunRef names an import started through the Billing API.
type BillingMigrationRunRef struct {
	ID uint `path:"id" required:"true"`
}

// MigrationRun reports an import's progress and report.
func (h *BillingHandler) MigrationRun(c *okapi.Context, req *BillingMigrationRunRef) error {
	if h.migration == nil {
		return c.AbortNotFound("no such migration")
	}
	run, err := h.migration.svc.Repo.FindByID(req.ID)
	if err != nil || run.Kind != models.BundleMigrationImport {
		return c.AbortNotFound("no such migration")
	}
	return ok(c, run)
}

// billingCoded gives a migration refusal the Billing API's coded form, so the console sees its code.
func billingCoded(err error) error {
	if me, isMig := err.(migrationError); isMig {
		return billing.NewCodedError(http.StatusForbidden, me.code, me.msg)
	}
	return billing.NewCodedError(http.StatusBadRequest, "MIGRATION_REFUSED", err.Error())
}

// BillingWorkspaceMigrationRef names an account's workspace for a migration call.
type BillingWorkspaceMigrationRef struct {
	ExternalID string `path:"external_id" required:"true"`
	Workspace  string `path:"workspace" required:"true" doc:"Workspace uid or handle"`
}

// A workspace not frozen by a migration is left as it is.
func (h *BillingHandler) MigrationUnfreeze(c *okapi.Context, req *BillingWorkspaceMigrationRef) error {
	return h.write(c, struct{}{}, func() (any, error) {
		if h.migration == nil || !h.migration.enabled {
			return nil, billing.NewCodedError(http.StatusForbidden, errMigrationOff.code, errMigrationOff.msg)
		}
		_, wsID, err := h.svc.MigrationAccount(req.ExternalID, req.Workspace)
		if err != nil {
			return nil, err
		}
		if err := h.migration.svc.UnfreezeMigration(c.Request().Context(), wsID); err != nil {
			return nil, err
		}
		h.record(c, "billing.workspace.migration_unfreeze", req.ExternalID, map[string]any{"workspace": req.Workspace})
		return map[string]any{"unfrozen": true}, nil
	})
}
