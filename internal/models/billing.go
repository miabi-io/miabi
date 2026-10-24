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

package models

import "time"

// BillingIdempotencyKey stores the answer to a Billing API write sent with an Idempotency-Key, so a
// retry within 24 hours gets the same answer instead of applying the change twice.
type BillingIdempotencyKey struct {
	Key         string    `json:"key" gorm:"primaryKey;size:200"`
	RequestHash string    `json:"request_hash" gorm:"not null"`
	Done        bool      `json:"done" gorm:"not null;default:false"`
	StatusCode  int       `json:"status_code" gorm:"not null;default:0"`
	Response    string    `json:"-" gorm:"type:text;not null;default:''"`
	ExpiresAt   time.Time `json:"expires_at" gorm:"not null;index"`
	CreatedAt   time.Time `json:"created_at"`
}

// Billing account statuses. A canceled account keeps its workspaces for the operator.
const (
	BillingAccountActive   = "active"
	BillingAccountCanceled = "canceled"
)

// Billing restriction levels, ordered none < warning < soft < hard.
const (
	BillingLevelNone    = "none"
	BillingLevelWarning = "warning"
	BillingLevelSoft    = "soft"
	BillingLevelHard    = "hard"
)

// BillingAccount is a billing platform's customer on this instance: it owns the workspaces billed
// to it and carries their subscription, which the platform pushes (plans/billing-api-v1.md).
type BillingAccount struct {
	ID             uint   `json:"id" gorm:"primaryKey"`
	ExternalID     string `json:"external_id" gorm:"size:200;uniqueIndex;not null"`
	OwnerUserID    uint   `json:"owner_user_id" gorm:"index;not null"`
	OrganizationID *uint  `json:"organization_id" gorm:"index"`
	Email          string `json:"email" gorm:"not null;default:''"`
	Name           string `json:"name" gorm:"not null;default:''"`
	PortalURL      string `json:"portal_url" gorm:"not null;default:''"`
	Status         string `json:"status" gorm:"type:varchar(20);not null;default:'active'"`
	// RestrictionLevel is the level last derived from the subscription and the forced hold.
	RestrictionLevel  string     `json:"restriction_level" gorm:"type:varchar(20);not null;default:'none'"`
	ForcedRestriction string     `json:"forced_restriction" gorm:"type:varchar(20);not null;default:''"`
	ForcedReason      string     `json:"forced_reason" gorm:"not null;default:''"`
	ForcedUntil       *time.Time `json:"forced_until"`
	// MaxWorkspaces caps the workspaces billed to the account; -1 is unlimited.
	MaxWorkspaces int `json:"max_workspaces" gorm:"not null;default:-1"`

	SubExternalID     string     `json:"sub_external_id" gorm:"not null;default:''"`
	PlanID            *uint      `json:"plan_id" gorm:"index"`
	SubStatus         string     `json:"sub_status" gorm:"type:varchar(30);not null;default:''"`
	CurrentPeriodEnd  *time.Time `json:"current_period_end"`
	CancelAtPeriodEnd bool       `json:"cancel_at_period_end" gorm:"not null;default:false"`
	GraceUntil        *time.Time `json:"grace_until"`
	EventAt           *time.Time `json:"event_at"`
	// Limits are the pushed add-on deltas, keyed by plan field (max_cpu_cores…).
	Limits map[string]int64 `json:"limits" gorm:"type:text;serializer:json"`
	// PushHash fingerprints the last push, so a resend with the same event_at is a no-op.
	PushHash   string     `json:"-" gorm:"not null;default:''"`
	CanceledAt *time.Time `json:"canceled_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// BillingEvent is an event for the billing platform, kept until delivered (an outbox). Its ID is
// the delivery sequence the platform deduplicates on.
type BillingEvent struct {
	ID            int64      `json:"sequence" gorm:"primaryKey;autoIncrement"`
	Type          string     `json:"type" gorm:"size:100;not null"`
	Data          string     `json:"-" gorm:"type:text;not null"`
	OccurredAt    time.Time  `json:"occurred_at" gorm:"not null"`
	Attempts      int        `json:"attempts" gorm:"not null;default:0"`
	NextAttemptAt time.Time  `json:"next_attempt_at" gorm:"not null;index"`
	DeliveredAt   *time.Time `json:"delivered_at" gorm:"index"`
	LastError     string     `json:"last_error" gorm:"type:text;not null;default:''"`
}
