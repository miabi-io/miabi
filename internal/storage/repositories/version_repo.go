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

package repositories

import (
	"errors"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// VersionRepository persists version policies and the versions they hand out.
type VersionRepository struct {
	db *gorm.DB
}

func NewVersionRepository(db *gorm.DB) *VersionRepository { return &VersionRepository{db: db} }

func (r *VersionRepository) CreatePolicy(p *models.VersionPolicy) error { return r.db.Create(p).Error }
func (r *VersionRepository) SavePolicy(p *models.VersionPolicy) error   { return r.db.Save(p).Error }

// DeletePolicy removes a policy and every version it handed out.
func (r *VersionRepository) DeletePolicy(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("policy_id = ?", id).Delete(&models.VersionAllocation{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.VersionPolicy{}, id).Error
	})
}

func (r *VersionRepository) FindPolicy(workspaceID, id uint) (*models.VersionPolicy, error) {
	var p models.VersionPolicy
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// FindPolicyByRef resolves a policy in a workspace from its numeric id or uid.
func (r *VersionRepository) FindPolicyByRef(workspaceID uint, ref string) (*models.VersionPolicy, error) {
	ref = strings.TrimSpace(ref)
	if n, err := strconv.ParseUint(ref, 10, 64); err == nil {
		return r.FindPolicy(workspaceID, uint(n))
	}
	if _, err := uuid.Parse(ref); err != nil {
		return nil, gorm.ErrRecordNotFound
	}
	var p models.VersionPolicy
	if err := r.db.Where("uid = ? AND workspace_id = ?", ref, workspaceID).First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// FindPolicyFor returns the policy owning the image repository of an app, or of an unbound pipeline.
func (r *VersionRepository) FindPolicyFor(appID, pipelineID *uint) (*models.VersionPolicy, error) {
	var p models.VersionPolicy
	q := r.db
	switch {
	case appID != nil:
		q = q.Where("application_id = ?", *appID)
	case pipelineID != nil:
		q = q.Where("pipeline_id = ?", *pipelineID)
	default:
		return nil, gorm.ErrRecordNotFound
	}
	if err := q.First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *VersionRepository) ListPolicies(workspaceID uint) ([]models.VersionPolicy, error) {
	var out []models.VersionPolicy
	err := r.db.Where("workspace_id = ?", workspaceID).Order("created_at DESC").Find(&out).Error
	return out, err
}

// Allocations returns every live allocation of a policy, oldest first.
func (r *VersionRepository) Allocations(policyID uint) ([]models.VersionAllocation, error) {
	var out []models.VersionAllocation
	err := r.db.Where("policy_id = ?", policyID).Order("id ASC").Find(&out).Error
	return out, err
}

// ListAllocations returns a page of a policy's allocations, newest first, plus the total.
func (r *VersionRepository) ListAllocations(policyID uint, limit, offset int) ([]models.VersionAllocation, int64, error) {
	var (
		out   []models.VersionAllocation
		total int64
	)
	q := r.db.Model(&models.VersionAllocation{}).Where("policy_id = ?", policyID)
	q.Count(&total)
	if err := q.Order("id DESC").Limit(limit).Offset(offset).Find(&out).Error; err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (r *VersionRepository) FindAllocationByRun(runID uint) (*models.VersionAllocation, error) {
	var a models.VersionAllocation
	if err := r.db.Where("pipeline_run_id = ?", runID).Order("id DESC").First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *VersionRepository) SaveAllocation(a *models.VersionAllocation) error {
	return r.db.Save(a).Error
}

// ErrAllocationConflict reports that another run took the version first; the caller re-plans.
var ErrAllocationConflict = errors.New("version allocated concurrently")

// Reserve plans and stores a version under a lock on the policy row, so runs on one policy are
// serialized. plan sees the policy's live allocations and returns the row to insert, or nil for none.
// The unique (policy, version) key is the backstop where the lock is unavailable (SQLite).
func (r *VersionRepository) Reserve(policyID uint, plan func(*models.VersionPolicy, []models.VersionAllocation) (*models.VersionAllocation, error)) (*models.VersionAllocation, error) {
	var out *models.VersionAllocation
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var p models.VersionPolicy
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&p, policyID).Error; err != nil {
			return err
		}
		var existing []models.VersionAllocation
		if err := tx.Where("policy_id = ?", policyID).Order("id ASC").Find(&existing).Error; err != nil {
			return err
		}
		a, err := plan(&p, existing)
		if err != nil || a == nil {
			return err
		}
		if err := tx.Create(a).Error; err != nil {
			if isDuplicateKey(err) {
				return ErrAllocationConflict
			}
			return err
		}
		out = a
		return nil
	})
	return out, err
}

// Release frees a failed run's version: deleted when it is still the head of its group so the next run
// reuses it, otherwise kept as abandoned. isHead decides with the live rows, under the policy lock.
func (r *VersionRepository) Release(a *models.VersionAllocation, isHead func(*models.VersionAllocation, []models.VersionAllocation) bool, note string) (deleted bool, err error) {
	err = r.db.Transaction(func(tx *gorm.DB) error {
		var p models.VersionPolicy
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&p, a.PolicyID).Error; err != nil {
			return err
		}
		var existing []models.VersionAllocation
		if err := tx.Where("policy_id = ?", a.PolicyID).Find(&existing).Error; err != nil {
			return err
		}
		if isHead(a, existing) {
			deleted = true
			return tx.Delete(&models.VersionAllocation{}, a.ID).Error
		}
		return tx.Model(&models.VersionAllocation{}).Where("id = ?", a.ID).
			Updates(map[string]any{"status": models.AllocationAbandoned, "note": note}).Error
	})
	return deleted, err
}

// Publish marks a built version published and raises the policy's displayed current version.
func (r *VersionRepository) Publish(a *models.VersionAllocation, current string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.VersionAllocation{}).Where("id = ?", a.ID).
			Updates(map[string]any{"status": models.AllocationPublished, "digest": a.Digest, "note": a.Note}).Error; err != nil {
			return err
		}
		if current == "" {
			return nil
		}
		return tx.Model(&models.VersionPolicy{}).Where("id = ?", a.PolicyID).Update("current", current).Error
	})
}

// RunRef is the pipeline and per-pipeline number of a run, for linking the allocation history.
type RunRef struct {
	PipelineID uint
	Number     int
}

// RunRefs maps run ids to their pipeline and number.
func (r *VersionRepository) RunRefs(runIDs []uint) (map[uint]RunRef, error) {
	out := map[uint]RunRef{}
	if len(runIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		ID         uint
		PipelineID uint
		Number     int
	}
	if err := r.db.Model(&models.PipelineRun{}).Select("id, pipeline_id, number").Where("id IN ?", runIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ID] = RunRef{PipelineID: row.PipelineID, Number: row.Number}
	}
	return out, nil
}

func isDuplicateKey(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") || strings.Contains(msg, "duplicate key") || strings.Contains(msg, "UNIQUE constraint failed")
}
