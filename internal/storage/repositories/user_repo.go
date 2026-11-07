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
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type UserRepository struct {
	db *gorm.DB
}

func NewUserRepository(db *gorm.DB) *UserRepository { return &UserRepository{db: db} }

func (r *UserRepository) Create(user *models.User) error {
	return r.db.Create(user).Error
}

func (r *UserRepository) Update(user *models.User) error {
	return r.db.Save(user).Error
}

// Uses Select so a nil limit clears the column back to "inherit the platform limit".
func (r *UserRepository) SetWorkspaceLimit(userID uint, limit *int) error {
	return r.db.Model(&models.User{}).Where("id = ?", userID).
		Select("workspace_limit").Update("workspace_limit", limit).Error
}

// SetCanCreateWorkspaces sets (or clears, when nil) a user's workspace creation override.
func (r *UserRepository) SetCanCreateWorkspaces(userID uint, allowed *bool) error {
	return r.db.Model(&models.User{}).Where("id = ?", userID).
		Select("can_create_workspaces").Update("can_create_workspaces", allowed).Error
}

func (r *UserRepository) SetWorkspaceMembershipLimit(userID uint, limit *int) error {
	return r.db.Model(&models.User{}).Where("id = ?", userID).
		Select("workspace_membership_limit").Update("workspace_membership_limit", limit).Error
}

func (r *UserRepository) FindByID(id uint) (*models.User, error) {
	var u models.User
	if err := r.db.First(&u, id).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *UserRepository) FindByEmail(email string) (*models.User, error) {
	var u models.User
	if err := r.db.Where("email = ?", strings.ToLower(strings.TrimSpace(email))).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *UserRepository) ExistsByEmail(email string) (bool, error) {
	var count int64
	err := r.db.Model(&models.User{}).
		Where("email = ?", strings.ToLower(strings.TrimSpace(email))).
		Count(&count).Error
	return count > 0, err
}

// FindByUsername resolves a user by their unique handle.
func (r *UserRepository) FindByUsername(username string) (*models.User, error) {
	var u models.User
	if err := r.db.Where("username = ?", strings.ToLower(strings.TrimSpace(username))).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// ExistsByUsername reports whether a username handle is already taken.
func (r *UserRepository) ExistsByUsername(username string) (bool, error) {
	var count int64
	err := r.db.Model(&models.User{}).
		Where("username = ?", strings.ToLower(strings.TrimSpace(username))).
		Count(&count).Error
	return count > 0, err
}

// Count leaves out service accounts: it decides whether a sign-up is the first, and so the platform admin.
func (r *UserRepository) Count() (int64, error) {
	var count int64
	err := r.db.Model(&models.User{}).Where("kind <> ?", models.UserKindService).Count(&count).Error
	return count, err
}

// A service account is left out even if its role says admin, since it is never treated as one.
func (r *UserRepository) CountByRole(role models.SystemRole) (int64, error) {
	var count int64
	err := r.db.Model(&models.User{}).Where("role = ? AND kind <> ?", role, models.UserKindService).Count(&count).Error
	return count, err
}

func (r *UserRepository) ListAdminIDs() ([]uint, error) {
	var ids []uint
	err := r.db.Model(&models.User{}).Where("role = ? AND kind <> ?", models.SystemRoleAdmin, models.UserKindService).
		Pluck("id", &ids).Error
	return ids, err
}

// FindByIDs returns the users with the given ids, in no particular order; unknown ids are skipped.
func (r *UserRepository) FindByIDs(ids []uint) ([]models.User, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var users []models.User
	err := r.db.Where("id IN ?", ids).Find(&users).Error
	return users, err
}

// ListServiceAccounts returns the active service accounts a workspace owns, oldest first.
func (r *UserRepository) ListServiceAccounts(workspaceID uint) ([]models.User, error) {
	var users []models.User
	err := r.db.Where("kind = ? AND service_workspace_id = ? AND active = ?", models.UserKindService, workspaceID, true).
		Order("created_at ASC").Find(&users).Error
	return users, err
}

// CountActive returns the number of active users.
func (r *UserRepository) CountActive() (int64, error) {
	var count int64
	err := r.db.Model(&models.User{}).Where("active = ?", true).Count(&count).Error
	return count, err
}

func (r *UserRepository) List(search, kind string, limit, offset int) ([]models.User, int64, error) {
	var (
		users []models.User
		total int64
	)
	q := r.db.Model(&models.User{})
	if kind != "" {
		q = q.Where("kind = ?", kind)
	}
	if s := strings.TrimSpace(search); s != "" {
		like := "%" + strings.ToLower(s) + "%"
		q = q.Where("LOWER(name) LIKE ? OR LOWER(username) LIKE ? OR LOWER(email) LIKE ?", like, like, like)
	}
	q.Count(&total)
	if err := q.Order("created_at DESC").Limit(limit).Offset(offset).Find(&users).Error; err != nil {
		return nil, 0, err
	}
	return users, total, nil
}

// Releasing owned organizations is best-effort: a purge must not fail on bookkeeping.
func (r *UserRepository) Delete(id uint) error {
	err := r.db.Model(&models.Organization{}).
		Where("owner_user_id = ?", id).
		Update("owner_user_id", 0).Error
	if err != nil {
		logger.Warn("could not release the organizations owned by a deleted user", "user", id, "error", err)
	}
	if err := r.db.Where("user_id = ?", id).Delete(&models.Passkey{}).Error; err != nil {
		logger.Warn("could not remove the passkeys of a deleted user", "user", id, "error", err)
	}
	return r.db.Delete(&models.User{}, id).Error
}

func (r *UserRepository) ListDueForDeletion(now time.Time) ([]models.User, error) {
	var users []models.User
	err := r.db.Where("scheduled_deletion_at IS NOT NULL AND scheduled_deletion_at <= ?", now).Find(&users).Error
	return users, err
}

// DefaultWorkspace falls back to the oldest membership when the stored default is stale and repairs it; nil if none.
func (r *UserRepository) DefaultWorkspace(userID uint) (*models.WorkspaceWithRole, error) {
	var user models.User
	if err := r.db.Select("id", "default_workspace_id").First(&user, userID).Error; err != nil {
		return nil, err
	}
	if user.DefaultWorkspaceID != nil {
		ws, err := r.workspaceForMember(userID, *user.DefaultWorkspaceID)
		if err == nil {
			return ws, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	ws, err := r.oldestMembership(userID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		if user.DefaultWorkspaceID != nil {
			_ = r.SetDefaultWorkspace(userID, nil)
		}
		return nil, nil
	}
	if err := r.SetDefaultWorkspace(userID, &ws.ID); err != nil {
		logger.Warn("failed to repair default workspace", "user", userID, "error", err)
	}
	return ws, nil
}

// The caller must check membership (see workspace.Service.SetDefault).
func (r *UserRepository) SetDefaultWorkspace(userID uint, workspaceID *uint) error {
	return r.db.Model(&models.User{}).Where("id = ?", userID).
		Update("default_workspace_id", workspaceID).Error
}

func (r *UserRepository) workspaceForMember(userID, workspaceID uint) (*models.WorkspaceWithRole, error) {
	var row models.WorkspaceWithRole
	err := r.db.Model(&models.Workspace{}).
		Select("workspaces.*, workspace_members.role AS role").
		Joins("JOIN workspace_members ON workspace_members.workspace_id = workspaces.id").
		Where("workspace_members.user_id = ? AND workspaces.id = ?", userID, workspaceID).
		First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// oldestMembership returns the workspace the user has belonged to longest.
func (r *UserRepository) oldestMembership(userID uint) (*models.WorkspaceWithRole, error) {
	var row models.WorkspaceWithRole
	err := r.db.Model(&models.Workspace{}).
		Select("workspaces.*, workspace_members.role AS role").
		Joins("JOIN workspace_members ON workspace_members.workspace_id = workspaces.id").
		Where("workspace_members.user_id = ?", userID).
		Order("workspace_members.created_at ASC, workspaces.id ASC").
		First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}
