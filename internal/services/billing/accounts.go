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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Subscription statuses of the contract vocabulary.
var subscriptionStatuses = map[string]bool{
	"incomplete": true, "incomplete_expired": true, "trialing": true, "active": true,
	"past_due": true, "unpaid": true, "canceled": true, "paused": true,
}

// Account errors, answered with the contract's codes.
var (
	ErrAccountNotFound  = coded(http.StatusNotFound, "BILLING_ACCOUNT_NOT_FOUND", "no billing account with this id")
	ErrAccountCanceled  = coded(http.StatusConflict, "BILLING_ACCOUNT_CANCELED", "this billing account is canceled; upsert it again to reopen it")
	ErrConflict         = coded(http.StatusConflict, "BILLING_CONFLICT", "another request changed this account at the same time; retry")
	ErrInvalidRequest   = coded(http.StatusBadRequest, "BILLING_INVALID_REQUEST", "the request is invalid")
	ErrOwnerInvalid     = coded(http.StatusBadRequest, "BILLING_OWNER_INVALID", "the owner needs a valid e-mail address")
	ErrOwnerProtected   = coded(http.StatusConflict, "BILLING_OWNER_PROTECTED", "the owner e-mail belongs to a platform admin or a service account")
	ErrOwnerInactive    = coded(http.StatusConflict, "BILLING_OWNER_INACTIVE", "the owner account is disabled")
	ErrOwnerOrgMismatch = coded(http.StatusConflict, "BILLING_OWNER_ORG_MISMATCH", "the owner belongs to another organization")
	ErrOwnerChange      = coded(http.StatusConflict, "BILLING_OWNER_CHANGE_UNSUPPORTED", "the owner of a billing account cannot be changed")
	ErrUnknownOrg       = coded(http.StatusBadRequest, "BILLING_UNKNOWN_ORGANIZATION", "no organization with this name")
	ErrUnknownStatus    = coded(http.StatusBadRequest, "BILLING_UNKNOWN_STATUS", "unknown subscription status")
	ErrUnknownPlan      = coded(http.StatusBadRequest, "BILLING_UNKNOWN_PLAN", "no plan with this name")
	ErrUnknownLimit     = coded(http.StatusBadRequest, "BILLING_UNKNOWN_LIMIT", "limits may only name numeric plan limits (max_…)")
	ErrStaleEvent       = coded(http.StatusConflict, "BILLING_STALE_EVENT", "this account already has a newer subscription state")
	ErrLimitsUnlicensed = coded(http.StatusPaymentRequired, "LICENSE_REQUIRED", "add-on limits need a licence with quota_override")
	ErrWorkspaceTaken   = coded(http.StatusConflict, "WORKSPACE_NAME_TAKEN", "a workspace with this name already exists")
	ErrWorkspaceNameBad = coded(http.StatusBadRequest, "BILLING_INVALID_REQUEST", "the workspace name is invalid or reserved")
	errMaxWorkspaces    = coded(http.StatusBadRequest, "BILLING_INVALID_REQUEST", "max_workspaces must be -1 (unlimited) or more")
)

// WorkspaceCreator makes a workspace for an owner with the handle taken exactly.
type WorkspaceCreator interface {
	CreateForOwner(ownerID uint, displayName, handle string) (*models.Workspace, error)
}

// UsageMeter reports a workspace's usage under the contract's Usage keys (apps, cpu_cores…) and
// its effective limits.
type UsageMeter interface {
	Measure(workspaceID uint) map[string]float64
	Limits(workspaceID uint) any
}

// AccountDeps are what accounts need beyond enrolment.
type AccountDeps struct {
	Workspaces WorkspaceCreator
	EE         enterprise.EE
	Meter      UsageMeter
	// OwnerCreated runs after commit for an owner this API created, to send the set-password e-mail.
	OwnerCreated func(u *models.User)
	// Restrictions applies the account's level to its workspaces; nil leaves them alone.
	Restrictions Restrictor
	Deleter      WorkspaceDeleter
	// HardAfter is how long past grace an unpaid account turns hard; 0 never.
	HardAfter time.Duration
}

// SetAccountDeps wires the account dependencies.
func (s *Service) SetAccountDeps(d AccountDeps) { s.deps = d }

// Owner identifies the account owner, matched by e-mail.
type Owner struct {
	Email       string `json:"email" required:"true"`
	Name        string `json:"name"`
	SSOProvider string `json:"sso_provider"`
	SSOSubject  string `json:"sso_subject"`
}

// CreateWorkspace asks for a new workspace; name is its handle, used exactly.
type CreateWorkspace struct {
	Name        string `json:"name" required:"true"`
	DisplayName string `json:"display_name"`
}

// AccountUpsert is the body of PUT /accounts/{external_id}.
type AccountUpsert struct {
	Email           string           `json:"email"`
	Name            string           `json:"name"`
	Organization    string           `json:"organization"`
	Owner           Owner            `json:"owner"`
	PortalURL       string           `json:"portal_url"`
	CreateWorkspace *CreateWorkspace `json:"create_workspace,omitempty"`
	MaxWorkspaces   *int             `json:"max_workspaces,omitempty" doc:"-1 = unlimited; omitted = unchanged"`
}

// SubscriptionPush is the body of PUT /accounts/{external_id}/subscription.
type SubscriptionPush struct {
	ExternalID        string           `json:"external_id"`
	Plan              string           `json:"plan" required:"true"`
	Status            string           `json:"status" required:"true"`
	CurrentPeriodEnd  *time.Time       `json:"current_period_end"`
	CancelAtPeriodEnd bool             `json:"cancel_at_period_end"`
	GraceUntil        *time.Time       `json:"grace_until"`
	EventAt           *time.Time       `json:"event_at" required:"true"`
	Limits            map[string]int64 `json:"limits" doc:"Add-on deltas on top of the plan"`
	MaxWorkspaces     *int             `json:"max_workspaces,omitempty" doc:"-1 = unlimited; omitted = unchanged"`
}

// Subscription is the subscription as an Account reports it.
type Subscription struct {
	ExternalID        string     `json:"external_id"`
	Plan              string     `json:"plan"`
	Status            string     `json:"status"`
	CurrentPeriodEnd  *time.Time `json:"current_period_end"`
	CancelAtPeriodEnd bool       `json:"cancel_at_period_end"`
	GraceUntil        *time.Time `json:"grace_until"`
	EventAt           *time.Time `json:"event_at"`
}

// AccountWorkspace is a workspace billed to an account.
type AccountWorkspace struct {
	UID          string `json:"uid"`
	Name         string `json:"name"`
	Slug         string `json:"slug"`
	DisplayName  string `json:"display_name"`
	Plan         string `json:"plan"`
	DashboardURL string `json:"dashboard_url"`
}

// Account is the contract's Account.
type Account struct {
	ExternalID        string             `json:"external_id"`
	Email             string             `json:"email"`
	Name              string             `json:"name"`
	Status            string             `json:"status"`
	RestrictionLevel  string             `json:"restriction_level"`
	ForcedRestriction string             `json:"forced_restriction"`
	ForcedReason      string             `json:"forced_reason"`
	ForcedUntil       *time.Time         `json:"forced_until"`
	MaxWorkspaces     int                `json:"max_workspaces"`
	Subscription      *Subscription      `json:"subscription"`
	Workspaces        []AccountWorkspace `json:"workspaces"`
	CreatedAt         time.Time          `json:"created_at"`
	UpdatedAt         time.Time          `json:"updated_at"`
}

// Violation is one limit a subscription change would break.
type Violation struct {
	Resource string  `json:"resource"`
	Used     float64 `json:"used"`
	Limit    float64 `json:"limit"`
}

// DryRunWorkspace lists the violations of one workspace.
type DryRunWorkspace struct {
	UID        string      `json:"uid"`
	Name       string      `json:"name"`
	Violations []Violation `json:"violations"`
}

// DryRun answers a ?dry_run=true subscription push.
type DryRun struct {
	Plan       string            `json:"plan"`
	Workspaces []DryRunWorkspace `json:"workspaces"`
}

var levelRank = map[string]int{models.BillingLevelNone: 0, models.BillingLevelWarning: 1, models.BillingLevelSoft: 2, models.BillingLevelHard: 3}

// DerivedLevel maps a subscription status to a restriction level (contract table): an unpaid
// account turns soft when its grace ends and hard hardAfter later (0 = never).
func DerivedLevel(status string, graceUntil *time.Time, now time.Time, hardAfter time.Duration) string {
	switch status {
	case "past_due", "unpaid", "incomplete":
		switch {
		case graceUntil == nil || now.Before(*graceUntil):
			return models.BillingLevelWarning
		case hardAfter > 0 && !now.Before(graceUntil.Add(hardAfter)):
			return models.BillingLevelHard
		}
		return models.BillingLevelSoft
	case "paused":
		return models.BillingLevelSoft
	case "canceled", "incomplete_expired":
		return models.BillingLevelHard
	}
	return models.BillingLevelNone
}

// EffectiveLevel is max(derived, forced); a forced hold past its until no longer counts, and a
// canceled account is always hard.
func EffectiveLevel(a *models.BillingAccount, now time.Time, hardAfter time.Duration) string {
	if a.Status == models.BillingAccountCanceled {
		return models.BillingLevelHard
	}
	level := DerivedLevel(a.SubStatus, a.GraceUntil, now, hardAfter)
	if f := a.ForcedRestriction; f != "" && (a.ForcedUntil == nil || now.Before(*a.ForcedUntil)) && levelRank[f] > levelRank[level] {
		level = f
	}
	return level
}

func validExternalID(id string) bool { return id != "" && len(id) <= 200 }

func utcSecond(t *time.Time) *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	v := t.UTC().Truncate(time.Second)
	return &v
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") || strings.Contains(msg, "duplicate key")
}

var lockingUpdate = clause.Locking{Strength: "UPDATE"}

func lockAccount(tx *gorm.DB, externalID string) (*models.BillingAccount, error) {
	var a models.BillingAccount
	err := tx.Clauses(lockingUpdate).Where("external_id = ?", externalID).First(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAccountNotFound
	}
	return &a, err
}

// GetAccount returns an account.
func (s *Service) GetAccount(externalID string) (*Account, error) {
	var a models.BillingAccount
	if err := s.db.Where("external_id = ?", externalID).First(&a).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAccountNotFound
		}
		return nil, err
	}
	return s.render(&a)
}

// UpsertAccount creates or updates an account. A new account finds or creates its owner; an
// existing one keeps it. create_workspace is honoured only while the account has no workspace.
func (s *Service) UpsertAccount(ctx context.Context, externalID string, in AccountUpsert) (*Account, error) {
	if !validExternalID(externalID) {
		return nil, ErrInvalidRequest
	}
	email := strings.ToLower(strings.TrimSpace(in.Owner.Email))
	if !validEmail(email) {
		return nil, ErrOwnerInvalid
	}
	if in.MaxWorkspaces != nil && *in.MaxWorkspaces < models.Unlimited {
		return nil, errMaxWorkspaces
	}
	orgID, defaultOrg, err := s.organization(in.Organization)
	if err != nil {
		return nil, err
	}

	var acct models.BillingAccount
	var newOwner *models.User
	err = s.db.Transaction(func(tx *gorm.DB) error {
		cur, err := lockAccount(tx, externalID)
		switch {
		case err == nil:
			var owner models.User
			if err := tx.First(&owner, cur.OwnerUserID).Error; err != nil {
				return err
			}
			if !strings.EqualFold(owner.Email, email) {
				return ErrOwnerChange
			}
			acct = *cur
		case errors.Is(err, ErrAccountNotFound):
			owner, created, err := s.resolveOwner(tx, in.Owner, email, orgID, defaultOrg)
			if err != nil {
				return err
			}
			if created {
				newOwner = owner
			}
			acct = models.BillingAccount{ExternalID: externalID, OwnerUserID: owner.ID, Status: models.BillingAccountActive,
				RestrictionLevel: models.BillingLevelNone, MaxWorkspaces: models.Unlimited}
			if orgID != 0 {
				acct.OrganizationID = &orgID
			}
		default:
			return err
		}
		acct.Email = strings.TrimSpace(in.Email)
		acct.Name = strings.TrimSpace(in.Name)
		acct.PortalURL = strings.TrimSpace(in.PortalURL)
		if in.MaxWorkspaces != nil {
			acct.MaxWorkspaces = *in.MaxWorkspaces
		}
		if acct.Status == models.BillingAccountCanceled {
			acct.Status, acct.CanceledAt = models.BillingAccountActive, nil
		}
		if err := s.updateLevel(tx, &acct); err != nil {
			return err
		}
		return tx.Save(&acct).Error
	})
	if isUniqueViolation(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	if newOwner != nil && s.deps.OwnerCreated != nil {
		s.deps.OwnerCreated(newOwner)
	}
	if in.CreateWorkspace != nil && strings.TrimSpace(in.CreateWorkspace.Name) != "" {
		err := s.db.Transaction(func(tx *gorm.DB) error {
			a, err := lockAccount(tx, externalID)
			if err != nil {
				return err
			}
			var n int64
			if err := tx.Model(&models.Workspace{}).Where("billing_account_id = ?", a.ID).Count(&n).Error; err != nil || n > 0 {
				return err
			}
			_, err = s.createLocked(tx, a, *in.CreateWorkspace)
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	s.enforce(ctx, &acct)
	return s.render(&acct)
}

func (s *Service) resolveOwner(tx *gorm.DB, o Owner, email string, orgID, defaultOrg uint) (*models.User, bool, error) {
	var u models.User
	err := tx.Where("lower(email) = ?", email).First(&u).Error
	if err == nil {
		switch {
		case u.IsService() || u.Role == models.SystemRoleAdmin:
			return nil, false, ErrOwnerProtected
		case !u.Active:
			return nil, false, ErrOwnerInactive
		}
		home := defaultOrg
		if u.OrganizationID != nil {
			home = *u.OrganizationID
		}
		if orgID != 0 && home != orgID {
			return nil, false, ErrOwnerOrgMismatch
		}
		return &u, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	hash, err := unusablePassword()
	if err != nil {
		return nil, false, err
	}
	now := s.now()
	u = models.User{Name: strings.TrimSpace(o.Name), Email: email, PasswordHash: hash, Role: models.SystemRoleUser,
		Active: true, EmailVerifiedAt: &now, SignupSource: models.SignupSourceBilling}
	if orgID != 0 && orgID != defaultOrg {
		u.OrganizationID = &orgID
	}
	if err := tx.Create(&u).Error; err != nil {
		return nil, false, err
	}
	if o.SSOProvider != "" && o.SSOSubject != "" {
		var p models.OAuthProvider
		if err := tx.Where("name = ?", o.SSOProvider).First(&p).Error; err != nil {
			return nil, false, coded(http.StatusBadRequest, "BILLING_OWNER_INVALID", "unknown sso_provider")
		}
		if err := tx.Create(&models.UserIdentity{ProviderID: p.ID, Subject: o.SSOSubject, UserID: u.ID}).Error; err != nil {
			return nil, false, err
		}
	}
	return &u, true, nil
}

// organization resolves a realm name ("" = the default) to its id, with the default's id. Both are
// 0 on an install without organizations.
func (s *Service) organization(name string) (id, defaultID uint, err error) {
	var def models.Organization
	if err := s.db.Where("is_default = ?", true).First(&def).Error; err == nil {
		defaultID = def.ID
	}
	name = strings.TrimSpace(name)
	if name == "" || name == models.DefaultOrganizationName {
		return defaultID, defaultID, nil
	}
	var org models.Organization
	if err := s.db.Where("name = ?", name).First(&org).Error; err != nil {
		return 0, 0, ErrUnknownOrg
	}
	return org.ID, defaultID, nil
}

// CancelAccount cancels an account: its workspaces are kept for the operator, under a hard
// restriction. Canceling a canceled account returns it unchanged.
func (s *Service) CancelAccount(ctx context.Context, externalID string) (*Account, error) {
	var acct *models.BillingAccount
	err := s.db.Transaction(func(tx *gorm.DB) error {
		a, err := lockAccount(tx, externalID)
		if err != nil {
			return err
		}
		acct = a
		if a.Status == models.BillingAccountCanceled {
			return nil
		}
		now := s.now()
		a.Status, a.CanceledAt = models.BillingAccountCanceled, &now
		if a.SubStatus != "" {
			a.SubStatus = "canceled"
		}
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

// PushResult is the answer of a subscription push: the account, or the dry run.
type PushResult struct {
	Account *Account
	DryRun  *DryRun
	// Changed is false when the push repeated the stored state.
	Changed bool
}

// PushSubscription applies a subscription push. Pushes are ordered by event_at: an older one is
// stale, and an equal one is a no-op when identical and stale otherwise.
func (s *Service) PushSubscription(ctx context.Context, externalID string, in SubscriptionPush, dryRun bool) (*PushResult, error) {
	if !subscriptionStatuses[in.Status] {
		return nil, ErrUnknownStatus
	}
	if in.EventAt == nil || in.EventAt.IsZero() {
		return nil, coded(http.StatusBadRequest, "BILLING_INVALID_REQUEST", "event_at is required")
	}
	if in.MaxWorkspaces != nil && *in.MaxWorkspaces < models.Unlimited {
		return nil, errMaxWorkspaces
	}
	plan, err := s.plans.FindByName(in.Plan)
	if err != nil || plan.System {
		return nil, ErrUnknownPlan
	}
	if err := validateLimits(in.Limits); err != nil {
		return nil, err
	}
	if len(in.Limits) > 0 && (s.deps.EE == nil || s.deps.EE.Require(enterprise.FlagQuotaOverride) != nil) {
		return nil, ErrLimitsUnlicensed
	}
	if dryRun {
		var a models.BillingAccount
		if err := s.db.Where("external_id = ?", externalID).First(&a).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrAccountNotFound
			}
			return nil, err
		}
		d, err := s.dryRun(&a, plan, in.Limits)
		if err != nil {
			return nil, err
		}
		return &PushResult{DryRun: d}, nil
	}

	hash := pushHash(in)
	eventAt := utcSecond(in.EventAt)
	changed := true
	var acct *models.BillingAccount
	err = s.db.Transaction(func(tx *gorm.DB) error {
		a, err := lockAccount(tx, externalID)
		if err != nil {
			return err
		}
		acct = a
		if a.Status == models.BillingAccountCanceled {
			return ErrAccountCanceled
		}
		if a.EventAt != nil {
			switch {
			case eventAt.Before(*a.EventAt):
				return ErrStaleEvent
			case eventAt.Equal(*a.EventAt) && hash == a.PushHash:
				changed = false
				return nil
			case eventAt.Equal(*a.EventAt):
				return ErrStaleEvent
			}
		}
		oldLimits := a.Limits
		a.SubExternalID = in.ExternalID
		a.PlanID = &plan.ID
		a.SubStatus = in.Status
		a.CurrentPeriodEnd = utcSecond(in.CurrentPeriodEnd)
		a.CancelAtPeriodEnd = in.CancelAtPeriodEnd
		a.GraceUntil = utcSecond(in.GraceUntil)
		a.EventAt = eventAt
		a.Limits = in.Limits
		a.PushHash = hash
		if in.MaxWorkspaces != nil {
			a.MaxWorkspaces = *in.MaxWorkspaces
		}
		if err := s.updateLevel(tx, a); err != nil {
			return err
		}
		if err := tx.Save(a).Error; err != nil {
			return err
		}
		var ids []uint
		if err := tx.Model(&models.Workspace{}).Where("billing_account_id = ?", a.ID).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err := tx.Model(&models.Workspace{}).Where("id IN ?", ids).Update("plan_id", plan.ID).Error; err != nil {
			return err
		}
		for _, id := range ids {
			if err := applyLimits(tx, id, plan, oldLimits, in.Limits); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.enforce(ctx, acct)
	out, err := s.render(acct)
	if err != nil {
		return nil, err
	}
	return &PushResult{Account: out, Changed: changed}, nil
}

func pushHash(in SubscriptionPush) string {
	in.EventAt = utcSecond(in.EventAt)
	in.CurrentPeriodEnd = utcSecond(in.CurrentPeriodEnd)
	in.GraceUntil = utcSecond(in.GraceUntil)
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// quotaLimitFields are the numeric limits a workspace quota override can carry.
var quotaLimitFields = func() map[string]bool {
	out := map[string]bool{}
	t := reflect.TypeOf(models.WorkspaceQuota{})
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if strings.HasPrefix(name, "max_") {
			out[name] = true
		}
	}
	return out
}()

func validateLimits(l map[string]int64) error {
	for k := range l {
		if !quotaLimitFields[k] {
			return ErrUnknownLimit
		}
	}
	return nil
}

// withDelta adds an add-on delta to a plan limit; unlimited stays unlimited and the sum never
// goes below zero.
func withDelta(base, delta int64) int64 {
	if base < 0 {
		return base
	}
	if v := base + delta; v > 0 {
		return v
	}
	return 0
}

// applyLimits writes the add-on deltas as the workspace's quota override: fields billing set
// before and no longer pushes are cleared, fields an admin set and billing does not manage stay.
func applyLimits(tx *gorm.DB, wsID uint, plan *models.Plan, oldLimits, newLimits map[string]int64) error {
	p := Plan{Limits: map[string]int64{}}
	if len(newLimits) > 0 {
		var err error
		if p, err = ToPlan(plan); err != nil {
			return err
		}
	}
	var q models.WorkspaceQuota
	if err := tx.Where("workspace_id = ?", wsID).First(&q).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	fields, err := quotaFields(&q)
	if err != nil {
		return err
	}
	for k := range oldLimits {
		if _, kept := newLimits[k]; !kept {
			delete(fields, k)
		}
	}
	for k, d := range newLimits {
		fields[k] = withDelta(p.Limits[k], d)
	}
	delete(fields, "workspace_id")
	delete(fields, "updated_at")
	if len(fields) == 0 {
		return tx.Where("workspace_id = ?", wsID).Delete(&models.WorkspaceQuota{}).Error
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	var next models.WorkspaceQuota
	if err := json.Unmarshal(raw, &next); err != nil {
		return err
	}
	next.WorkspaceID = wsID
	return tx.Save(&next).Error
}

func quotaFields(q *models.WorkspaceQuota) (map[string]any, error) {
	raw, err := json.Marshal(q)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	return out, json.Unmarshal(raw, &out)
}

// dryRun reports the limits each workspace of the account would break under plan and limits.
func (s *Service) dryRun(acct *models.BillingAccount, plan *models.Plan, limits map[string]int64) (*DryRun, error) {
	p, err := ToPlan(plan)
	if err != nil {
		return nil, err
	}
	var wss []models.Workspace
	if err := s.db.Where("billing_account_id = ?", acct.ID).Order("id").Find(&wss).Error; err != nil {
		return nil, err
	}
	out := &DryRun{Plan: plan.Name, Workspaces: []DryRunWorkspace{}}
	for _, ws := range wss {
		target := map[string]int64{}
		for k, v := range p.Limits {
			target[k] = v
		}
		var q models.WorkspaceQuota
		if err := s.db.Where("workspace_id = ?", ws.ID).First(&q).Error; err == nil {
			fields, err := quotaFields(&q)
			if err != nil {
				return nil, err
			}
			for k, v := range fields {
				if _, billed := acct.Limits[k]; billed || !quotaLimitFields[k] {
					continue
				}
				if n, ok := v.(float64); ok {
					target[k] = int64(n)
				}
			}
		}
		for k, d := range limits {
			target[k] = withDelta(p.Limits[k], d)
		}
		dw := DryRunWorkspace{UID: ws.UID, Name: ws.Name, Violations: []Violation{}}
		if s.deps.Meter != nil {
			for res, used := range s.deps.Meter.Measure(ws.ID) {
				if lim, ok := target["max_"+res]; ok && lim >= 0 && used > float64(lim) {
					dw.Violations = append(dw.Violations, Violation{Resource: "max_" + res, Used: used, Limit: float64(lim)})
				}
			}
			sort.Slice(dw.Violations, func(i, j int) bool { return dw.Violations[i].Resource < dw.Violations[j].Resource })
		}
		out.Workspaces = append(out.Workspaces, dw)
	}
	return out, nil
}

func (s *Service) render(a *models.BillingAccount) (*Account, error) {
	out := &Account{
		ExternalID: a.ExternalID, Email: a.Email, Name: a.Name, Status: a.Status,
		RestrictionLevel: s.level(a), ForcedRestriction: a.ForcedRestriction, ForcedReason: a.ForcedReason,
		ForcedUntil: a.ForcedUntil, MaxWorkspaces: a.MaxWorkspaces, Workspaces: []AccountWorkspace{},
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
	planNames := map[uint]string{}
	planName := func(id *uint) string {
		if id == nil {
			return ""
		}
		if n, ok := planNames[*id]; ok {
			return n
		}
		if p, err := s.plans.FindByID(*id); err == nil {
			planNames[*id] = p.Name
		}
		return planNames[*id]
	}
	if a.SubStatus != "" {
		out.Subscription = &Subscription{ExternalID: a.SubExternalID, Plan: planName(a.PlanID), Status: a.SubStatus,
			CurrentPeriodEnd: a.CurrentPeriodEnd, CancelAtPeriodEnd: a.CancelAtPeriodEnd, GraceUntil: a.GraceUntil, EventAt: a.EventAt}
	}
	var wss []models.Workspace
	if err := s.db.Where("billing_account_id = ?", a.ID).Order("id").Find(&wss).Error; err != nil {
		return nil, err
	}
	base := strings.TrimRight(s.info.MiabiURL, "/")
	for _, ws := range wss {
		out.Workspaces = append(out.Workspaces, AccountWorkspace{
			UID: ws.UID, Name: ws.Name, Slug: ws.Name, DisplayName: ws.DisplayName, Plan: planName(ws.PlanID),
			DashboardURL: base + "/?workspace=" + url.QueryEscape(ws.Name),
		})
	}
	return out, nil
}

func validEmail(s string) bool {
	at := strings.LastIndex(s, "@")
	return at > 0 && at < len(s)-1 && !strings.ContainsAny(s, " \t\r\n")
}
