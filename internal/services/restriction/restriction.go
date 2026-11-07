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

// Package restriction holds admin workspace restrictions and the gate every workload-changing entry point consults.
package restriction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/account"
)

// Action is what a caller is about to do in a workspace.
type Action int

const (
	// ActionWrite is a change made through the API or a create counted by quotas.
	ActionWrite Action = iota
	// ActionDeploy is an automated deploy: git push, GitOps sync, scheduled or pushed pipelines.
	ActionDeploy
	// ActionRun keeps workloads running: control-manager redeploys and CronJob schedules.
	ActionRun
)

// Error refuses an action in a restricted workspace.
type Error struct {
	State  models.WorkspaceRestriction
	Reason string
	// Billing marks a restriction the billing platform set (the account's payment status).
	Billing bool
}

func (e *Error) Error() string {
	what := "read-only"
	switch e.State {
	case models.WorkspaceRestrictionSuspended:
		what = "suspended"
	case models.WorkspaceRestrictionFrozen:
		msg := "this workspace is frozen for a migration"
		if e.Reason != "" {
			msg += ": " + e.Reason
		}
		return msg
	}
	by := " by the platform administrator"
	if e.Billing {
		by = " because of its billing status"
	}
	msg := "this workspace is " + what + by
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	return msg
}

// Code is the stable machine code surfaced in the API error envelope.
func (e *Error) Code() string {
	if e.Billing {
		return "BILLING_RESTRICTED"
	}
	switch e.State {
	case models.WorkspaceRestrictionSuspended:
		return "WORKSPACE_SUSPENDED"
	case models.WorkspaceRestrictionFrozen:
		return "WORKSPACE_FROZEN"
	}
	return "WORKSPACE_READ_ONLY"
}

// Errors returned to admins.
var (
	ErrInvalidState = errors.New("state must be read_only or suspended")
	ErrSystem       = errors.New("the platform's system workspace cannot be restricted")
	ErrUntilPast    = errors.New("until must be in the future")
	ErrFrozen       = errors.New("this workspace is frozen for a migration: unfreeze it, or lift the restriction, first")
	ErrRestricted   = errors.New("this workspace is already restricted by the platform administrator")
)

// Repo is the workspace storage the service needs.
type Repo interface {
	FindByID(id uint) (*models.Workspace, error)
	SetRestriction(id uint, fields map[string]any) error
	ListRestrictionsDue(now time.Time) ([]models.Workspace, error)
}

// Workloads stops and starts a workspace's apps and databases (*account.Service).
type Workloads interface {
	StopWorkspace(ctx context.Context, wsID uint) (account.StopResult, models.FrozenResources)
	StopWorkspaceApps(ctx context.Context, wsID uint) (account.StopResult, models.FrozenResources)
	StartWorkspace(ctx context.Context, wsID uint, only *models.FrozenResources) (account.StartResult, error)
}

// Bounds how stale another replica's view can be; local changes invalidate at once.
const cacheTTL = 15 * time.Second

type entry struct {
	state   models.WorkspaceRestriction
	reason  string
	billing bool
	at      time.Time
}

// Service manages workspace restrictions.
type Service struct {
	repo      Repo
	workloads Workloads
	now       func() time.Time

	mu      sync.Mutex
	cache   map[uint]entry
	changed func(ctx context.Context, wsID uint)
	// billingActor returns the billing service user's id (0 when there is none).
	billingActor func() uint
}

// SetBillingActor tells restrictions set by the billing service user apart from an admin's.
func (s *Service) SetBillingActor(f func() uint) { s.billingActor = f }

func (s *Service) SetOnChange(f func(ctx context.Context, wsID uint)) { s.changed = f }

func (s *Service) notify(ctx context.Context, wsID uint) {
	if s.changed != nil {
		s.changed(ctx, wsID)
	}
}

// NewService builds the service.
func NewService(repo Repo, workloads Workloads) *Service {
	return &Service{repo: repo, workloads: workloads, now: time.Now, cache: map[uint]entry{}}
}

func (s *Service) lookup(wsID uint) entry {
	now := s.now()
	s.mu.Lock()
	e, ok := s.cache[wsID]
	s.mu.Unlock()
	if ok && now.Sub(e.at) < cacheTTL {
		return e
	}
	e = entry{at: now}
	if ws, err := s.repo.FindByID(wsID); err == nil {
		e.state, e.reason = ws.Restriction, ws.RestrictionReason
		if ws.RestrictedBy != nil && s.billingActor != nil {
			id := s.billingActor()
			e.billing = id != 0 && *ws.RestrictedBy == id
		}
	}
	s.mu.Lock()
	s.cache[wsID] = e
	s.mu.Unlock()
	return e
}

func (s *Service) forget(wsID uint) {
	s.mu.Lock()
	delete(s.cache, wsID)
	s.mu.Unlock()
}

// Allowed reports whether action may proceed in the workspace. A nil service allows everything.
func (s *Service) Allowed(wsID uint, a Action) error {
	if s == nil || wsID == 0 {
		return nil
	}
	e := s.lookup(wsID)
	switch e.state {
	case models.WorkspaceRestrictionReadOnly:
		if a == ActionRun {
			return nil
		}
	case models.WorkspaceRestrictionSuspended, models.WorkspaceRestrictionFrozen:
	default:
		return nil
	}
	return &Error{State: e.state, Reason: e.reason, Billing: e.billing}
}

// SetInput is a restriction to apply.
type SetInput struct {
	State  models.WorkspaceRestriction `json:"state" enum:"read_only,suspended"`
	Reason string                      `json:"reason" maxLength:"500"`
	Until  *time.Time                  `json:"until"`
}

// Set restricts a workspace; suspending stops and remembers workloads, moving to read-only restarts them.
func (s *Service) Set(ctx context.Context, wsID, actorID uint, in SetInput) (*models.Workspace, error) {
	if !in.State.Valid() {
		return nil, ErrInvalidState
	}
	now := s.now().UTC()
	if in.Until != nil && !in.Until.After(now) {
		return nil, ErrUntilPast
	}
	ws, err := s.repo.FindByID(wsID)
	if err != nil {
		return nil, err
	}
	if ws.System {
		return nil, ErrSystem
	}
	if ws.Restriction == models.WorkspaceRestrictionFrozen {
		return nil, ErrFrozen
	}
	fields := map[string]any{
		"restriction": in.State, "restriction_reason": strings.TrimSpace(in.Reason), "restriction_until": in.Until,
		"restricted_at": now, "restricted_by": actorID,
	}
	// The state is written first, so nothing the gate guards restarts a workload while it is being stopped.
	if err := s.repo.SetRestriction(wsID, fields); err != nil {
		return nil, err
	}
	s.forget(wsID)
	switch {
	case in.State == models.WorkspaceRestrictionSuspended && ws.Restriction != models.WorkspaceRestrictionSuspended:
		res, frozen := s.workloads.StopWorkspace(ctx, wsID)
		raw, err := json.Marshal(frozen)
		if err != nil {
			return nil, err
		}
		if err := s.repo.SetRestriction(wsID, map[string]any{"frozen_resources": string(raw)}); err != nil {
			return nil, err
		}
		logger.Info("workspace suspended", "workspace", wsID, "apps", res.Apps, "databases", res.Databases)
	case in.State == models.WorkspaceRestrictionReadOnly && ws.Restriction == models.WorkspaceRestrictionSuspended:
		if err := s.restart(ctx, ws); err != nil {
			return nil, err
		}
	}
	s.notify(ctx, wsID)
	return s.repo.FindByID(wsID)
}

// Lift removes a restriction, restarting what a suspension stopped.
func (s *Service) Lift(ctx context.Context, wsID uint) (*models.Workspace, error) {
	ws, err := s.repo.FindByID(wsID)
	if err != nil {
		return nil, err
	}
	if err := s.repo.SetRestriction(wsID, map[string]any{
		"restriction": models.WorkspaceRestrictionNone, "restriction_reason": "", "restriction_until": nil,
		"restricted_at": nil, "restricted_by": nil,
	}); err != nil {
		return nil, err
	}
	s.forget(wsID)
	if ws.Restriction == models.WorkspaceRestrictionSuspended || ws.Restriction == models.WorkspaceRestrictionFrozen {
		if err := s.restart(ctx, ws); err != nil {
			return nil, err
		}
	}
	s.notify(ctx, wsID)
	return s.repo.FindByID(wsID)
}

// Freeze stops a workspace's apps for a migration, keeping databases up so they can be exported.
func (s *Service) Freeze(ctx context.Context, wsID, actorID uint, reason string) error {
	ws, err := s.repo.FindByID(wsID)
	if err != nil {
		return err
	}
	if ws.System {
		return ErrSystem
	}
	switch ws.Restriction {
	case models.WorkspaceRestrictionFrozen:
		return s.SetFrozenReason(wsID, reason)
	case models.WorkspaceRestrictionNone:
	default:
		return ErrRestricted
	}
	if err := s.repo.SetRestriction(wsID, map[string]any{
		"restriction": models.WorkspaceRestrictionFrozen, "restriction_reason": strings.TrimSpace(reason),
		"restriction_until": nil, "restricted_at": s.now().UTC(), "restricted_by": actorID,
	}); err != nil {
		return err
	}
	s.forget(wsID)
	res, frozen := s.workloads.StopWorkspaceApps(ctx, wsID)
	raw, err := json.Marshal(frozen)
	if err != nil {
		return err
	}
	if err := s.repo.SetRestriction(wsID, map[string]any{"frozen_resources": string(raw)}); err != nil {
		return err
	}
	logger.Info("workspace frozen for a migration", "workspace", wsID, "apps", res.Apps)
	s.notify(ctx, wsID)
	return nil
}

// SetFrozenReason rewords a migration freeze, such as when the workspace is retired after a move.
func (s *Service) SetFrozenReason(wsID uint, reason string) error {
	if err := s.repo.SetRestriction(wsID, map[string]any{"restriction_reason": strings.TrimSpace(reason)}); err != nil {
		return err
	}
	s.forget(wsID)
	return nil
}

// Unfreeze only lifts a migration freeze, so an administrator's restriction is never lifted this way.
func (s *Service) Unfreeze(ctx context.Context, wsID uint) error {
	ws, err := s.repo.FindByID(wsID)
	if err != nil {
		return err
	}
	if ws.Restriction != models.WorkspaceRestrictionFrozen {
		return nil
	}
	_, err = s.Lift(ctx, wsID)
	return err
}

func (s *Service) restart(ctx context.Context, ws *models.Workspace) error {
	frozen := ws.FrozenResources
	if frozen == nil {
		frozen = &models.FrozenResources{}
	}
	res, err := s.workloads.StartWorkspace(ctx, ws.ID, frozen)
	if err != nil {
		return err
	}
	if res.Failed > 0 {
		logger.Warn("workspace restart after suspension: some workloads did not start", "workspace", ws.ID, "failed", res.Failed)
	}
	return s.repo.SetRestriction(ws.ID, map[string]any{"frozen_resources": nil})
}

// ExpireDue lifts restrictions whose end date has passed.
func (s *Service) ExpireDue(ctx context.Context) error {
	due, err := s.repo.ListRestrictionsDue(s.now())
	if err != nil {
		return err
	}
	var errs []error
	for i := range due {
		if _, err := s.Lift(ctx, due[i].ID); err != nil {
			errs = append(errs, fmt.Errorf("workspace %d: %w", due[i].ID, err))
			continue
		}
		logger.Info("workspace restriction expired", "workspace", due[i].ID, "state", due[i].Restriction)
	}
	return errors.Join(errs...)
}
