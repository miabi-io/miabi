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
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/workspace"
	"gorm.io/gorm"
)

// Workspace errors, answered with the contract's codes.
var (
	ErrWorkspaceLimit    = coded(http.StatusConflict, "BILLING_WORKSPACE_LIMIT", "the account already has max_workspaces workspaces")
	ErrLastWorkspace     = coded(http.StatusConflict, "BILLING_LAST_WORKSPACE", "this is the account's last workspace; cancel the subscription instead")
	ErrConfirmMismatch   = coded(http.StatusBadRequest, "BILLING_CONFIRM_MISMATCH", "confirm must equal the workspace handle")
	ErrWorkspaceNotOwned = coded(http.StatusForbidden, "BILLING_WORKSPACE_NOT_OWNED", "the workspace is not owned by the account owner")
	ErrWorkspaceAttached = coded(http.StatusConflict, "BILLING_WORKSPACE_ATTACHED", "the workspace is billed to another account")
	ErrOverrideConflict  = coded(http.StatusConflict, "BILLING_OVERRIDE_CONFLICT", "an admin quota override sets a limit billing manages; clear it first")
	ErrWorkspaceNotFound = coded(http.StatusNotFound, "BILLING_WORKSPACE_NOT_FOUND", "no such workspace in this account")
	errAttachRequest     = coded(http.StatusBadRequest, "BILLING_INVALID_REQUEST", "send exactly one of workspace or create")
	errDisplayName       = coded(http.StatusBadRequest, "BILLING_INVALID_REQUEST", "display_name must not be empty")
)

// WorkspaceDeleter tears a workspace down at once (*account.Service).
type WorkspaceDeleter interface {
	DeleteWorkspaceNow(ctx context.Context, wsID uint) error
}

// WorkspaceAttach is the body of POST /accounts/{external_id}/workspaces.
type WorkspaceAttach struct {
	Workspace string           `json:"workspace,omitempty" doc:"uid or handle of a workspace the owner already has"`
	Create    *CreateWorkspace `json:"create,omitempty"`
}

// WorkspaceRename is the body of PATCH /accounts/{external_id}/workspaces/{workspace}.
type WorkspaceRename struct {
	DisplayName string `json:"display_name" required:"true" maxLength:"100"`
}

// WorkspaceDelete is the body of POST /accounts/{external_id}/workspaces/{workspace}/delete.
type WorkspaceDelete struct {
	Confirm string `json:"confirm" required:"true" doc:"The workspace handle, typed again"`
}

// WorkspaceUsage is one workspace of a Usage report.
type WorkspaceUsage struct {
	UID    string             `json:"uid"`
	Name   string             `json:"name"`
	Used   map[string]float64 `json:"used"`
	Limits any                `json:"limits"`
}

// Usage is the answer of GET /accounts/{external_id}/usage.
type Usage struct {
	ExternalID string           `json:"external_id"`
	At         time.Time        `json:"at"`
	Workspaces []WorkspaceUsage `json:"workspaces"`
}

// findWorkspace resolves a uid or handle among any workspaces.
func findWorkspace(tx *gorm.DB, ref string) (*models.Workspace, error) {
	ref = strings.TrimSpace(ref)
	q := tx.Where("name = ?", ref)
	if _, err := uuid.Parse(ref); err == nil {
		q = tx.Where("uid = ?", ref)
	}
	var ws models.Workspace
	if err := q.First(&ws).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrWorkspaceNotFound
		}
		return nil, err
	}
	return &ws, nil
}

// accountWorkspace resolves a uid or handle among the account's workspaces.
func accountWorkspace(tx *gorm.DB, a *models.BillingAccount, ref string) (*models.Workspace, error) {
	ws, err := findWorkspace(tx, ref)
	if err != nil {
		return nil, err
	}
	if ws.BillingAccountID == nil || *ws.BillingAccountID != a.ID {
		return nil, ErrWorkspaceNotFound
	}
	return ws, nil
}

func countBilled(tx *gorm.DB, a *models.BillingAccount) (int64, error) {
	var n int64
	err := tx.Model(&models.Workspace{}).Where("billing_account_id = ?", a.ID).Count(&n).Error
	return n, err
}

func checkLimit(tx *gorm.DB, a *models.BillingAccount) error {
	if a.MaxWorkspaces < 0 {
		return nil
	}
	n, err := countBilled(tx, a)
	if err != nil {
		return err
	}
	if n >= int64(a.MaxWorkspaces) {
		return ErrWorkspaceLimit
	}
	return nil
}

// refuseLast keeps an active account from losing its last workspace; the platform cancels the
// subscription instead.
func refuseLast(tx *gorm.DB, a *models.BillingAccount) error {
	if a.Status == models.BillingAccountCanceled {
		return nil
	}
	n, err := countBilled(tx, a)
	if err != nil {
		return err
	}
	if n <= 1 {
		return ErrLastWorkspace
	}
	return nil
}

// AddWorkspace creates a workspace for the account or attaches one its owner already has.
func (s *Service) AddWorkspace(ctx context.Context, externalID string, in WorkspaceAttach) (*Account, error) {
	create := in.Create != nil && strings.TrimSpace(in.Create.Name) != ""
	if create == (strings.TrimSpace(in.Workspace) != "") {
		return nil, errAttachRequest
	}
	var acct *models.BillingAccount
	var wsID uint
	err := s.db.Transaction(func(tx *gorm.DB) error {
		a, err := lockAccount(tx, externalID)
		if err != nil {
			return err
		}
		acct = a
		if a.Status == models.BillingAccountCanceled {
			return ErrAccountCanceled
		}
		if create {
			wsID, err = s.createLocked(tx, a, *in.Create)
		} else {
			wsID, err = s.attachLocked(tx, a, in.Workspace)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	s.enforce(ctx, acct, wsID)
	return s.render(acct)
}

// createLocked makes a workspace for the account owner and bills it, under the account's lock so
// max_workspaces holds. A same-named workspace already billed to the account is the answer to a
// retry; one the owner holds unbilled is what an earlier attempt left and is taken over.
func (s *Service) createLocked(tx *gorm.DB, a *models.BillingAccount, in CreateWorkspace) (uint, error) {
	handle := strings.TrimSpace(in.Name)
	existing, err := findWorkspace(tx, handle)
	switch {
	case err == nil && existing.BillingAccountID != nil && *existing.BillingAccountID == a.ID:
		return existing.ID, nil
	case err == nil && (existing.OwnerID != a.OwnerUserID || existing.BillingAccountID != nil || existing.System):
		return 0, ErrWorkspaceTaken
	case err != nil && !errors.Is(err, ErrWorkspaceNotFound):
		return 0, err
	}
	if err := checkLimit(tx, a); err != nil {
		return 0, err
	}
	if existing != nil {
		return existing.ID, s.bindWorkspace(tx, a, existing.ID, existing.PlanID)
	}
	if s.deps.Workspaces == nil {
		return 0, errors.New("workspace creation is not wired")
	}
	ws, err := s.deps.Workspaces.CreateForOwner(a.OwnerUserID, strings.TrimSpace(in.DisplayName), handle)
	switch {
	case errors.Is(err, workspace.ErrNameTaken):
		return 0, ErrWorkspaceTaken
	case errors.Is(err, workspace.ErrNameInvalid), errors.Is(err, workspace.ErrNameReserved):
		return 0, ErrWorkspaceNameBad
	case err != nil:
		return 0, err
	}
	return ws.ID, s.bindWorkspace(tx, a, ws.ID, nil)
}

func (s *Service) attachLocked(tx *gorm.DB, a *models.BillingAccount, ref string) (uint, error) {
	ws, err := findWorkspace(tx, ref)
	if err != nil {
		return 0, err
	}
	switch {
	case ws.BillingAccountID != nil && *ws.BillingAccountID == a.ID:
		return ws.ID, nil
	case ws.BillingAccountID != nil:
		return 0, ErrWorkspaceAttached
	case ws.System || ws.OwnerID != a.OwnerUserID:
		return 0, ErrWorkspaceNotOwned
	}
	if len(a.Limits) > 0 {
		var q models.WorkspaceQuota
		if err := tx.Where("workspace_id = ?", ws.ID).First(&q).Error; err == nil {
			fields, err := quotaFields(&q)
			if err != nil {
				return 0, err
			}
			for k := range a.Limits {
				if v, set := fields[k]; set && v != nil {
					return 0, ErrOverrideConflict
				}
			}
		}
	}
	if err := checkLimit(tx, a); err != nil {
		return 0, err
	}
	return ws.ID, s.bindWorkspace(tx, a, ws.ID, ws.PlanID)
}

// bindWorkspace bills a workspace to the account and gives it the account's plan and add-ons;
// prevPlan is restored when it is detached.
func (s *Service) bindWorkspace(tx *gorm.DB, a *models.BillingAccount, wsID uint, prevPlan *uint) error {
	updates := map[string]any{"billing_account_id": a.ID, "billing_prev_plan_id": prevPlan}
	if a.PlanID != nil {
		updates["plan_id"] = *a.PlanID
	}
	if err := tx.Model(&models.Workspace{}).Where("id = ?", wsID).Updates(updates).Error; err != nil {
		return err
	}
	if a.PlanID == nil || len(a.Limits) == 0 {
		return nil
	}
	plan, err := s.plans.FindByID(*a.PlanID)
	if err != nil {
		return err
	}
	return applyLimits(tx, wsID, plan, nil, a.Limits)
}

// DetachWorkspace stops billing a workspace: its earlier plan comes back, and billing's add-ons
// and restriction are removed. The workspace and its owner are untouched.
func (s *Service) DetachWorkspace(ctx context.Context, externalID, ref string) (*Account, error) {
	var acct *models.BillingAccount
	var ws *models.Workspace
	err := s.db.Transaction(func(tx *gorm.DB) error {
		a, err := lockAccount(tx, externalID)
		if err != nil {
			return err
		}
		acct = a
		if ws, err = accountWorkspace(tx, a, ref); err != nil {
			return err
		}
		if err := refuseLast(tx, a); err != nil {
			return err
		}
		if err := tx.Model(&models.Workspace{}).Where("id = ?", ws.ID).Updates(map[string]any{
			"billing_account_id": nil, "billing_prev_plan_id": nil, "plan_id": ws.BillingPrevPlanID,
		}).Error; err != nil {
			return err
		}
		return applyLimits(tx, ws.ID, nil, a.Limits, nil)
	})
	if err != nil {
		return nil, err
	}
	if actor := s.ServiceUserID(); s.deps.Restrictions != nil && actor != 0 && ws.Restriction != models.WorkspaceRestrictionNone &&
		ws.RestrictedBy != nil && *ws.RestrictedBy == actor {
		if _, err := s.deps.Restrictions.Lift(ctx, ws.ID); err != nil {
			return nil, err
		}
	}
	return s.render(acct)
}

// RenameWorkspace changes a workspace's display name; its handle never changes.
func (s *Service) RenameWorkspace(externalID, ref string, in WorkspaceRename) (*Account, error) {
	name := strings.TrimSpace(in.DisplayName)
	if name == "" {
		return nil, errDisplayName
	}
	var acct *models.BillingAccount
	err := s.db.Transaction(func(tx *gorm.DB) error {
		a, err := lockAccount(tx, externalID)
		if err != nil {
			return err
		}
		acct = a
		ws, err := accountWorkspace(tx, a, ref)
		if err != nil {
			return err
		}
		return tx.Model(&models.Workspace{}).Where("id = ?", ws.ID).Update("display_name", name).Error
	})
	if err != nil {
		return nil, err
	}
	return s.render(acct)
}

// DeleteWorkspace deletes a workspace at once, with everything in it. confirm must repeat its
// handle. The account's lock is held throughout, so two deletes cannot both pass the last-workspace
// check.
func (s *Service) DeleteWorkspace(ctx context.Context, externalID, ref string, in WorkspaceDelete) (*Account, *models.Workspace, error) {
	if s.deps.Deleter == nil {
		return nil, nil, errors.New("workspace deletion is not wired")
	}
	var acct *models.BillingAccount
	var ws *models.Workspace
	err := s.db.Transaction(func(tx *gorm.DB) error {
		a, err := lockAccount(tx, externalID)
		if err != nil {
			return err
		}
		acct = a
		if ws, err = accountWorkspace(tx, a, ref); err != nil {
			return err
		}
		if strings.TrimSpace(in.Confirm) != ws.Name {
			return ErrConfirmMismatch
		}
		if err := refuseLast(tx, a); err != nil {
			return err
		}
		return s.deps.Deleter.DeleteWorkspaceNow(ctx, ws.ID)
	})
	if err != nil {
		return nil, nil, err
	}
	out, err := s.render(acct)
	return out, ws, err
}

// AccountUsage reports each workspace's usage against its effective limits.
func (s *Service) AccountUsage(externalID string) (*Usage, error) {
	var a models.BillingAccount
	if err := s.db.Where("external_id = ?", externalID).First(&a).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAccountNotFound
		}
		return nil, err
	}
	var wss []models.Workspace
	if err := s.db.Where("billing_account_id = ?", a.ID).Order("id").Find(&wss).Error; err != nil {
		return nil, err
	}
	out := &Usage{ExternalID: a.ExternalID, At: s.now().UTC().Truncate(time.Second), Workspaces: []WorkspaceUsage{}}
	for _, ws := range wss {
		u := WorkspaceUsage{UID: ws.UID, Name: ws.Name, Used: map[string]float64{}}
		if s.deps.Meter != nil {
			u.Used = s.deps.Meter.Measure(ws.ID)
			u.Limits = s.deps.Meter.Limits(ws.ID)
		}
		out.Workspaces = append(out.Workspaces, u)
	}
	return out, nil
}
