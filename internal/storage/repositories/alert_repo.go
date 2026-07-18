// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AlertRepository persists workspace-level, deduplicated alerts. The active-state
// partial unique index (migration) guarantees at most one open alert per
// (workspace, dedup_key); this repo folds repeats into it and drives the FSM.
type AlertRepository struct {
	db *gorm.DB
}

func NewAlertRepository(db *gorm.DB) *AlertRepository { return &AlertRepository{db: db} }

// ActiveByDedup returns the open (firing/acknowledged) alert for a dedup key, or
// gorm.ErrRecordNotFound.
func (r *AlertRepository) ActiveByDedup(workspaceID uint, dedupKey string) (*models.Alert, error) {
	var a models.Alert
	err := r.db.Where("workspace_id = ? AND dedup_key = ? AND state IN ?",
		workspaceID, dedupKey, []models.AlertState{models.AlertFiring, models.AlertAcknowledged}).
		First(&a).Error
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// Fire creates a new alert or folds a repeat signal into the existing active one
// (bumping Count/LastSeen and refreshing the detail). It returns the alert and
// whether it was newly created (a caller notifies on creation, not on every
// fold). Done in a transaction under a row lock so concurrent signals for the
// same condition can't create duplicates.
func (r *AlertRepository) Fire(in *models.Alert) (*models.Alert, bool, error) {
	var out *models.Alert
	created := false
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var existing models.Alert
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("workspace_id = ? AND dedup_key = ? AND state IN ?",
				in.WorkspaceID, in.DedupKey, []models.AlertState{models.AlertFiring, models.AlertAcknowledged}).
			First(&existing).Error
		switch {
		case err == gorm.ErrRecordNotFound:
			in.Count = 1
			in.FirstSeen = in.LastSeen
			if err := tx.Create(in).Error; err != nil {
				return err
			}
			out, created = in, true
			return nil
		case err != nil:
			return err
		default:
			existing.Count++
			existing.LastSeen = in.LastSeen
			existing.Severity = in.Severity
			existing.Title = in.Title
			existing.Body = in.Body
			if in.Labels != nil {
				existing.Labels = in.Labels
			}
			if err := tx.Save(&existing).Error; err != nil {
				return err
			}
			out = &existing
			return nil
		}
	})
	return out, created, err
}

// Resolve transitions the active alert for a dedup key to resolved, returning it
// (or gorm.ErrRecordNotFound when nothing was open — a no-op recovery signal).
func (r *AlertRepository) Resolve(workspaceID uint, dedupKey string, at time.Time) (*models.Alert, error) {
	var out *models.Alert
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var a models.Alert
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("workspace_id = ? AND dedup_key = ? AND state IN ?",
				workspaceID, dedupKey, []models.AlertState{models.AlertFiring, models.AlertAcknowledged}).
			First(&a).Error
		if err != nil {
			return err
		}
		a.State = models.AlertResolved
		a.ResolvedAt = &at
		a.LastSeen = at
		if err := tx.Save(&a).Error; err != nil {
			return err
		}
		out = &a
		return nil
	})
	return out, err
}

// SetState transitions an alert by id within a workspace (ack/resolve from the
// UI). Returns the updated alert.
func (r *AlertRepository) SetState(workspaceID, id uint, state models.AlertState) (*models.Alert, error) {
	var a models.Alert
	if err := r.db.Where("workspace_id = ? AND id = ?", workspaceID, id).First(&a).Error; err != nil {
		return nil, err
	}
	a.State = state
	if state == models.AlertResolved {
		now := time.Now().UTC()
		a.ResolvedAt = &now
	}
	if err := r.db.Save(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// ListByWorkspace returns alerts for a workspace, newest first, optionally
// filtered to active only.
func (r *AlertRepository) ListByWorkspace(workspaceID uint, activeOnly bool, limit int) ([]models.Alert, error) {
	q := r.db.Where("workspace_id = ?", workspaceID)
	if activeOnly {
		q = q.Where("state IN ?", []models.AlertState{models.AlertFiring, models.AlertAcknowledged})
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var alerts []models.Alert
	err := q.Order("last_seen DESC").Limit(limit).Find(&alerts).Error
	return alerts, err
}

// ListActiveByCategory returns every open (firing/acknowledged) alert of a
// category across all workspaces — the scanner uses it to resolve conditions that
// no longer hold (e.g. a cert that was renewed).
func (r *AlertRepository) ListActiveByCategory(category models.AlertCategory) ([]models.Alert, error) {
	var alerts []models.Alert
	err := r.db.Where("category = ? AND state IN ?", category,
		[]models.AlertState{models.AlertFiring, models.AlertAcknowledged}).
		Find(&alerts).Error
	return alerts, err
}

// ArchiveResolvedBefore archives resolved alerts older than `before` (retention).
func (r *AlertRepository) ArchiveResolvedBefore(before time.Time) (int64, error) {
	res := r.db.Model(&models.Alert{}).
		Where("state = ? AND resolved_at < ?", models.AlertResolved, before).
		Update("state", models.AlertArchived)
	return res.RowsAffected, res.Error
}
