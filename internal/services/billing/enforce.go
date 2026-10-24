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
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/restriction"
	"gorm.io/gorm"
)

// Restrictor sets and lifts workspace restrictions (*restriction.Service).
type Restrictor interface {
	Set(ctx context.Context, wsID, actorID uint, in restriction.SetInput) (*models.Workspace, error)
	Lift(ctx context.Context, wsID uint) (*models.Workspace, error)
}

// SuspendRequest is the body of POST /accounts/{external_id}/suspend: the platform's own hold.
type SuspendRequest struct {
	Reason string     `json:"reason" maxLength:"500"`
	Level  string     `json:"level" required:"true" enum:"soft,hard"`
	Until  *time.Time `json:"until"`
}

var errSuspendLevel = coded(http.StatusBadRequest, "BILLING_INVALID_REQUEST", "level must be soft or hard, and until in the future")

// ServiceUserID returns the billing service user's id without creating it; 0 when it does not
// exist yet. Restrictions recorded with this id are billing's own.
func (s *Service) ServiceUserID() uint {
	if id := s.userID.Load(); id != 0 {
		return uint(id)
	}
	u, err := s.users.FindByEmail(ServiceUserEmail)
	if err != nil {
		return 0
	}
	s.userID.Store(uint64(u.ID))
	return u.ID
}

func (s *Service) level(a *models.BillingAccount) string {
	return EffectiveLevel(a, s.now(), s.deps.HardAfter)
}

// updateLevel recomputes the account's level and queues a billing.restriction.changed event in
// the same transaction when it moved.
func (s *Service) updateLevel(tx *gorm.DB, a *models.BillingAccount) error {
	from, to := a.RestrictionLevel, s.level(a)
	a.RestrictionLevel = to
	if from == to || a.ID == 0 {
		return nil
	}
	return enqueueEvent(tx, EventRestrictionChanged, RestrictionChanged{ExternalID: a.ExternalID, From: from, To: to}, s.now())
}

// workspaceState maps an account level to the restriction its workspaces carry.
func workspaceState(level string) models.WorkspaceRestriction {
	switch level {
	case models.BillingLevelSoft:
		return models.WorkspaceRestrictionReadOnly
	case models.BillingLevelHard:
		return models.WorkspaceRestrictionSuspended
	}
	return models.WorkspaceRestrictionNone
}

func restrictionReason(a *models.BillingAccount, now time.Time) string {
	if a.ForcedRestriction != "" && (a.ForcedUntil == nil || now.Before(*a.ForcedUntil)) && a.RestrictionLevel == a.ForcedRestriction {
		if r := strings.TrimSpace(a.ForcedReason); r != "" {
			return r
		}
		return "on hold by the billing provider"
	}
	if a.Status == models.BillingAccountCanceled {
		return "the subscription is canceled"
	}
	switch a.SubStatus {
	case "past_due", "unpaid", "incomplete":
		return "payment is overdue"
	case "paused":
		return "the subscription is paused"
	case "canceled":
		return "the subscription is canceled"
	case "incomplete_expired":
		return "the subscription expired before its first payment"
	}
	return ""
}

// enforce brings the account's workspaces to its level. Billing changes or lifts only restrictions
// it set itself: an admin's hold is left in place, and the account's level applies once it is
// lifted (the sweep comes back to it). Failures are logged; the sweep retries them.
func (s *Service) enforce(ctx context.Context, a *models.BillingAccount, only ...uint) {
	if s.deps.Restrictions == nil {
		return
	}
	actor := s.ServiceUserID()
	if actor == 0 {
		var err error
		if actor, err = s.serviceUserIDCreating(); err != nil {
			logger.Error("billing enforcement: no service user", "error", err)
			return
		}
	}
	q := s.db.Where("billing_account_id = ?", a.ID)
	if len(only) > 0 {
		q = q.Where("id IN ?", only)
	}
	var wss []models.Workspace
	if err := q.Order("id").Find(&wss).Error; err != nil {
		logger.Error("billing enforcement: list workspaces", "account", a.ExternalID, "error", err)
		return
	}
	want, reason := workspaceState(a.RestrictionLevel), restrictionReason(a, s.now())
	for _, ws := range wss {
		if ws.System {
			continue
		}
		ours := ws.RestrictedBy != nil && *ws.RestrictedBy == actor
		if ws.Restriction != models.WorkspaceRestrictionNone && !ours {
			continue
		}
		var err error
		switch {
		case want == models.WorkspaceRestrictionNone && ws.Restriction != models.WorkspaceRestrictionNone:
			_, err = s.deps.Restrictions.Lift(ctx, ws.ID)
		case want != models.WorkspaceRestrictionNone && (ws.Restriction != want || ws.RestrictionReason != reason):
			_, err = s.deps.Restrictions.Set(ctx, ws.ID, actor, restriction.SetInput{State: want, Reason: reason})
		}
		if err != nil {
			logger.Error("billing enforcement failed", "account", a.ExternalID, "workspace", ws.ID, "want", want, "error", err)
		}
	}
}

func (s *Service) serviceUserIDCreating() (uint, error) {
	u, err := s.EnsureServiceUser()
	if err != nil {
		return 0, err
	}
	s.userID.Store(uint64(u.ID))
	return u.ID, nil
}

// SuspendAccount places the platform's own hold on an account (forced restriction).
func (s *Service) SuspendAccount(ctx context.Context, externalID string, in SuspendRequest) (*Account, error) {
	if (in.Level != models.BillingLevelSoft && in.Level != models.BillingLevelHard) || (in.Until != nil && !in.Until.After(s.now())) {
		return nil, errSuspendLevel
	}
	return s.changeAccount(ctx, externalID, func(a *models.BillingAccount) {
		a.ForcedRestriction, a.ForcedReason, a.ForcedUntil = in.Level, strings.TrimSpace(in.Reason), utcSecond(in.Until)
	})
}

// ResumeAccount lifts the platform's own hold; the subscription's level still applies.
func (s *Service) ResumeAccount(ctx context.Context, externalID string) (*Account, error) {
	return s.changeAccount(ctx, externalID, func(a *models.BillingAccount) {
		a.ForcedRestriction, a.ForcedReason, a.ForcedUntil = "", "", nil
	})
}

func (s *Service) changeAccount(ctx context.Context, externalID string, edit func(a *models.BillingAccount)) (*Account, error) {
	var acct *models.BillingAccount
	err := s.db.Transaction(func(tx *gorm.DB) error {
		a, err := lockAccount(tx, externalID)
		if err != nil {
			return err
		}
		acct = a
		edit(a)
		if err := s.updateLevel(tx, a); err != nil {
			return err
		}
		return tx.Save(a).Error
	})
	if err != nil {
		return nil, err
	}
	s.enforce(ctx, acct)
	return s.render(acct)
}

// Sweep recomputes every account's level, which moves with time (grace periods, the hard-after
// delay, forced holds ending), and re-applies it to the workspaces.
func (s *Service) Sweep(ctx context.Context) error {
	var ids []uint
	if err := s.db.Model(&models.BillingAccount{}).Order("id").Pluck("id", &ids).Error; err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		var acct models.BillingAccount
		err := s.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Clauses(lockingUpdate).First(&acct, id).Error; err != nil {
				return err
			}
			expired := acct.ForcedUntil != nil && !s.now().Before(*acct.ForcedUntil)
			if expired {
				acct.ForcedRestriction, acct.ForcedReason, acct.ForcedUntil = "", "", nil
			}
			before := acct.RestrictionLevel
			if err := s.updateLevel(tx, &acct); err != nil {
				return err
			}
			if before == acct.RestrictionLevel && !expired {
				return nil
			}
			return tx.Save(&acct).Error
		})
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				errs = append(errs, fmt.Errorf("account %d: %w", id, err))
			}
			continue
		}
		s.enforce(ctx, &acct)
	}
	return errors.Join(errs...)
}
