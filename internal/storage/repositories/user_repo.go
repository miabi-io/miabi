// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"strings"
	"time"

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

// SetWorkspaceLimit sets (or clears, when limit is nil) a user's per-user
// workspace-count override. Uses Select so a nil clears the column back to
// "inherit the platform limit".
func (r *UserRepository) SetWorkspaceLimit(userID uint, limit *int) error {
	return r.db.Model(&models.User{}).Where("id = ?", userID).
		Select("workspace_limit").Update("workspace_limit", limit).Error
}

// SetWorkspaceMembershipLimit sets (or clears, when nil) a user's per-user
// workspace-membership override — the join counterpart of SetWorkspaceLimit.
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

func (r *UserRepository) Count() (int64, error) {
	var count int64
	err := r.db.Model(&models.User{}).Count(&count).Error
	return count, err
}

// CountByRole returns the number of users with the given system role.
func (r *UserRepository) CountByRole(role models.SystemRole) (int64, error) {
	var count int64
	err := r.db.Model(&models.User{}).Where("role = ?", role).Count(&count).Error
	return count, err
}

// ListAdminIDs returns the user ids of the platform super-admins — the recipients
// of platform-scoped alerts (node offline, engine too old, license).
func (r *UserRepository) ListAdminIDs() ([]uint, error) {
	var ids []uint
	err := r.db.Model(&models.User{}).Where("role = ?", models.SystemRoleAdmin).
		Pluck("id", &ids).Error
	return ids, err
}

// CountActive returns the number of active users.
func (r *UserRepository) CountActive() (int64, error) {
	var count int64
	err := r.db.Model(&models.User{}).Where("active = ?", true).Count(&count).Error
	return count, err
}

// List returns users matching an optional search term (name/username/email),
// newest first, with the total count for pagination.
func (r *UserRepository) List(search string, limit, offset int) ([]models.User, int64, error) {
	var (
		users []models.User
		total int64
	)
	q := r.db.Model(&models.User{})
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

// Delete removes a user by id.
func (r *UserRepository) Delete(id uint) error {
	return r.db.Delete(&models.User{}, id).Error
}

// ListDueForDeletion returns accounts whose scheduled deletion time has passed,
// for the purge job.
func (r *UserRepository) ListDueForDeletion(now time.Time) ([]models.User, error) {
	var users []models.User
	err := r.db.Where("scheduled_deletion_at IS NOT NULL AND scheduled_deletion_at <= ?", now).Find(&users).Error
	return users, err
}
