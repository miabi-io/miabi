// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// SecurityPolicyRepository stores Security Center policies and the decisions they record.
type SecurityPolicyRepository struct{ db *gorm.DB }

func NewSecurityPolicyRepository(db *gorm.DB) *SecurityPolicyRepository {
	return &SecurityPolicyRepository{db: db}
}

// ListByKind returns every scope's rule for one kind.
func (r *SecurityPolicyRepository) ListByKind(kind string) ([]models.SecurityPolicy, error) {
	var out []models.SecurityPolicy
	err := r.db.Where("kind = ?", kind).Order("scope_type, scope_id").Find(&out).Error
	return out, err
}

func (r *SecurityPolicyRepository) ListAll() ([]models.SecurityPolicy, error) {
	var out []models.SecurityPolicy
	err := r.db.Order("kind, scope_type, scope_id").Find(&out).Error
	return out, err
}

func (r *SecurityPolicyRepository) FindByID(id uint) (*models.SecurityPolicy, error) {
	var p models.SecurityPolicy
	if err := r.db.First(&p, id).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// FindScope loads the rule for one kind at one scope.
func (r *SecurityPolicyRepository) FindScope(kind, scopeType string, scopeID uint) (*models.SecurityPolicy, error) {
	var p models.SecurityPolicy
	err := r.db.Where("kind = ? AND scope_type = ? AND scope_id = ?", kind, scopeType, scopeID).First(&p).Error
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *SecurityPolicyRepository) Save(p *models.SecurityPolicy) error { return r.db.Save(p).Error }

func (r *SecurityPolicyRepository) Delete(id uint) error {
	return r.db.Delete(&models.SecurityPolicy{}, id).Error
}

func (r *SecurityPolicyRepository) CreateEvent(e *models.SecurityEvent) error {
	return r.db.Create(e).Error
}

// EventFilter narrows an event listing. Zero values match everything.
type EventFilter struct {
	Kind        string
	Decision    string
	WorkspaceID uint
	Since       time.Time
	BeforeID    uint
	Limit       int
}

// ListEvents returns matching events, newest first.
func (r *SecurityPolicyRepository) ListEvents(f EventFilter) ([]models.SecurityEvent, error) {
	q := r.db.Model(&models.SecurityEvent{})
	if f.Kind != "" {
		q = q.Where("kind = ?", f.Kind)
	}
	if f.Decision != "" {
		q = q.Where("decision = ?", f.Decision)
	}
	if f.WorkspaceID != 0 {
		q = q.Where("workspace_id = ?", f.WorkspaceID)
	}
	if !f.Since.IsZero() {
		q = q.Where("created_at >= ?", f.Since)
	}
	if f.BeforeID != 0 {
		q = q.Where("id < ?", f.BeforeID)
	}
	if f.Limit > 0 {
		q = q.Limit(f.Limit)
	}
	var out []models.SecurityEvent
	err := q.Order("id DESC").Find(&out).Error
	return out, err
}

// EventCount is one kind/decision bucket of a count.
type EventCount struct {
	Kind     string `json:"kind"`
	Decision string `json:"decision"`
	Count    int64  `json:"count"`
}

// CountEventsSince buckets events by kind and decision.
func (r *SecurityPolicyRepository) CountEventsSince(since time.Time) ([]EventCount, error) {
	var out []EventCount
	err := r.db.Model(&models.SecurityEvent{}).Select("kind, decision, count(*) AS count").
		Where("created_at >= ?", since).Group("kind, decision").Scan(&out).Error
	return out, err
}

// PruneEvents removes events older than cutoff.
func (r *SecurityPolicyRepository) PruneEvents(cutoff time.Time) (int64, error) {
	res := r.db.Where("created_at < ?", cutoff).Delete(&models.SecurityEvent{})
	return res.RowsAffected, res.Error
}
