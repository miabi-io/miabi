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

package billing

import (
	"net/http"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
)

// EventWorkspaceDeleted reports that a billed workspace no longer exists.
const EventWorkspaceDeleted = "billing.workspace.deleted"

// WorkspaceDeletedEvent is the data of billing.workspace.deleted.
type WorkspaceDeletedEvent struct {
	ExternalID string `json:"external_id"`
	Workspace  string `json:"workspace"`
	UID        string `json:"uid"`
}

// ErrWorkspaceManagedByConsole refuses deleting a billed workspace from Miabi in console mode.
var ErrWorkspaceManagedByConsole = coded(http.StatusConflict, "WORKSPACE_MANAGED_BY_CONSOLE",
	"this workspace is billed through Miabi Console; delete it there")

// SetConsoleOnly wires whether workspace creation and deletion belong to the console
// (MIABI_WORKSPACE_CREATION=console).
func (s *Service) SetConsoleOnly(fn func() bool) { s.consoleOnly = fn }

// GuardWorkspaceDeletion refuses a Miabi-side delete of a billed workspace in console mode: the
// console would keep charging for a workspace that no longer exists.
func (s *Service) GuardWorkspaceDeletion(ws *models.Workspace) error {
	if s.consoleOnly != nil && s.consoleOnly() && ws.BillingAccountID != nil {
		return ErrWorkspaceManagedByConsole
	}
	return nil
}

// WorkspaceDeleted queues billing.workspace.deleted for a billed workspace, so the console learns
// of a delete it did not make.
func (s *Service) WorkspaceDeleted(ws *models.Workspace) {
	if ws == nil || ws.BillingAccountID == nil {
		return
	}
	var a models.BillingAccount
	if err := s.db.Select("id", "external_id").First(&a, *ws.BillingAccountID).Error; err != nil {
		return
	}
	if err := enqueueEvent(s.db, EventWorkspaceDeleted, WorkspaceDeletedEvent{ExternalID: a.ExternalID, Workspace: ws.Name, UID: ws.UID}, s.now()); err != nil {
		logger.Warn("billing: queue workspace.deleted failed", "workspace", ws.ID, "error", err)
	}
}
