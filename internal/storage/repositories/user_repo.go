// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// DefaultWorkspace resolves where a client with no workspace in mind should land,
// with the caller's role in it.
//
// Membership is re-checked on every read, so a default naming a workspace the user
// has left — or one that was deleted — never dead-ends the console. It falls back to
// the user's OLDEST membership, which is almost always their primary workspace (the
// membership list is ordered newest-first for display, which is the wrong guess
// here), and repairs the stored value in place.
//
// Returns (nil, nil) when the user belongs to no workspace at all.
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
		// No membership left to point at; drop the stale pointer rather than keep
		// handing back a workspace the user cannot open.
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

// SetDefaultWorkspace records where header-less clients land. Nil clears it. The
// caller is responsible for checking membership — see workspace.Service.SetDefault.
func (r *UserRepository) SetDefaultWorkspace(userID uint, workspaceID *uint) error {
	return r.db.Model(&models.User{}).Where("id = ?", userID).
		Update("default_workspace_id", workspaceID).Error
}

// workspaceForMember returns the workspace and the caller's role in it, or
// gorm.ErrRecordNotFound when they are not a member.
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
