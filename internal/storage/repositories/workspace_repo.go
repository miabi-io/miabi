// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type WorkspaceRepository struct {
	db *gorm.DB
}

func NewWorkspaceRepository(db *gorm.DB) *WorkspaceRepository { return &WorkspaceRepository{db: db} }

// CreateWithOwner creates a workspace and its owner membership atomically.
func (r *WorkspaceRepository) CreateWithOwner(ws *models.Workspace) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(ws).Error; err != nil {
			return err
		}
		member := &models.WorkspaceMember{
			WorkspaceID: ws.ID,
			UserID:      ws.OwnerID,
			Role:        models.WorkspaceRoleOwner,
		}
		return tx.Create(member).Error
	})
}

func (r *WorkspaceRepository) Update(ws *models.Workspace) error {
	return r.db.Save(ws).Error
}

// Delete hard-deletes a workspace and every resource belonging to it in one transaction, scoped
// by workspace_id directly or via a parent, so nothing is left orphaned leaking encrypted
// secrets. Order matters: rows holding a foreign key go before the rows they reference.
func (r *WorkspaceRepository) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		// Fresh subquery builders for children scoped by a parent in the workspace.
		appIDs := func() *gorm.DB { return tx.Model(&models.Application{}).Select("id").Where("workspace_id = ?", id) }
		instIDs := func() *gorm.DB {
			return tx.Model(&models.DatabaseInstance{}).Select("id").Where("workspace_id = ?", id)
		}
		volIDs := func() *gorm.DB { return tx.Model(&models.Volume{}).Select("id").Where("workspace_id = ?", id) }
		domIDs := func() *gorm.DB { return tx.Model(&models.Domain{}).Select("id").Where("workspace_id = ?", id) }
		stackIDs := func() *gorm.DB { return tx.Model(&models.Stack{}).Select("id").Where("workspace_id = ?", id) }
		runIDs := func() *gorm.DB { return tx.Model(&models.PipelineRun{}).Select("id").Where("workspace_id = ?", id) }
		dbIDs := func() *gorm.DB {
			return tx.Model(&models.Database{}).Select("id").Where("instance_id IN (?)", instIDs())
		}
		srcIDs := func() *gorm.DB {
			return tx.Unscoped().Model(&models.TemplateSource{}).Select("id").Where("workspace_id = ?", id)
		}

		// many2many join tables (no model) — raw deletes by parent subquery.
		joins := []string{
			"DELETE FROM application_networks WHERE application_id IN (SELECT id FROM applications WHERE workspace_id = ?)",
			"DELETE FROM database_instance_networks WHERE database_instance_id IN (SELECT id FROM database_instances WHERE workspace_id = ?)",
		}
		for _, sql := range joins {
			if err := tx.Exec(sql, id).Error; err != nil {
				return err
			}
		}

		// Ordered deletes: each entry is (model, where-clause, args). Children first,
		// then the tables they reference, ending at the workspace row.
		type del struct {
			model any
			where string
			args  []any
		}
		steps := []del{
			{&models.AppEnvVar{}, "application_id IN (?)", []any{appIDs()}},
			{&models.AppPort{}, "application_id IN (?)", []any{appIDs()}},
			{&models.Deployment{}, "application_id IN (?)", []any{appIDs()}},
			{&models.Release{}, "application_id IN (?)", []any{appIDs()}},
			{&models.AppEvent{}, "application_id IN (?)", []any{appIDs()}},
			{&models.MetricSample{}, "application_id IN (?)", []any{appIDs()}},
			{&models.PortBinding{}, "workspace_id = ?", []any{id}},
			{&models.Job{}, "workspace_id = ?", []any{id}},
			{&models.Backup{}, "database_id IN (?)", []any{dbIDs()}},
			{&models.BackupSchedule{}, "workspace_id = ?", []any{id}},
			{&models.Database{}, "instance_id IN (?)", []any{instIDs()}},
			{&models.VolumeBackup{}, "volume_id IN (?)", []any{volIDs()}},
			{&models.DNSRecord{}, "domain_id IN (?)", []any{domIDs()}},
			{&models.PipelineStepRun{}, "pipeline_run_id IN (?)", []any{runIDs()}},
			{&models.PipelineRun{}, "workspace_id = ?", []any{id}},
			{&models.Image{}, "workspace_id = ?", []any{id}},
			{&models.StackEnvVar{}, "stack_id IN (?)", []any{stackIDs()}},
			{&models.WebhookDelivery{}, "workspace_id = ?", []any{id}},
			{&models.ReleaseApproval{}, "workspace_id = ?", []any{id}},
			{&models.TemplateInstall{}, "workspace_id = ?", []any{id}},
			// Routes reference certificates, so they go before them.
			{&models.Route{}, "workspace_id = ?", []any{id}},
			// Top-level workspace-scoped resources. Applications go before the
			// registries/git-repos/stacks they reference; domains before providers.
			{&models.Application{}, "workspace_id = ?", []any{id}},
			{&models.DatabaseInstance{}, "workspace_id = ?", []any{id}},
			{&models.Volume{}, "workspace_id = ?", []any{id}},
			{&models.Certificate{}, "workspace_id = ?", []any{id}},
			{&models.Domain{}, "workspace_id = ?", []any{id}},
			{&models.DNSProvider{}, "workspace_id = ?", []any{id}},
			{&models.Registry{}, "workspace_id = ?", []any{id}},
			{&models.GitRepository{}, "workspace_id = ?", []any{id}},
			{&models.GitSource{}, "workspace_id = ?", []any{id}},
			{&models.Stack{}, "workspace_id = ?", []any{id}},
			{&models.Network{}, "workspace_id = ?", []any{id}},
			{&models.Secret{}, "workspace_id = ?", []any{id}},
			{&models.Environment{}, "workspace_id = ?", []any{id}},
			{&models.Middleware{}, "workspace_id = ?", []any{id}},
			{&models.Webhook{}, "workspace_id = ?", []any{id}},
			{&models.NotificationChannel{}, "workspace_id = ?", []any{id}},
			{&models.PipelineDefinition{}, "workspace_id = ?", []any{id}},
			// Templates are scoped to a source (no workspace_id column). Delete this
			// workspace's custom-source templates first, then the sources themselves.
			{&models.Template{}, "source_id IN (?)", []any{srcIDs()}},
			{&models.TemplateSource{}, "workspace_id = ?", []any{id}},
			{&models.WorkspaceKey{}, "workspace_id = ?", []any{id}},
			{&models.WorkspaceBackupSettings{}, "workspace_id = ?", []any{id}},
			{&models.WorkspaceMember{}, "workspace_id = ?", []any{id}},
			{&models.WorkspaceInvitation{}, "workspace_id = ?", []any{id}},
		}
		// Unscoped forces a hard DELETE even for soft-delete models (e.g. templates):
		// a workspace teardown must remove the rows, not just set deleted_at.
		for _, s := range steps {
			if err := tx.Unscoped().Where(s.where, s.args...).Delete(s.model).Error; err != nil {
				return err
			}
		}
		return tx.Unscoped().Delete(&models.Workspace{}, id).Error
	})
}

func (r *WorkspaceRepository) FindByID(id uint) (*models.Workspace, error) {
	var ws models.Workspace
	if err := r.db.First(&ws, id).Error; err != nil {
		return nil, err
	}
	return &ws, nil
}

// FindSystem returns the built-in platform system workspace, if it exists.
func (r *WorkspaceRepository) FindSystem() (*models.Workspace, error) {
	var ws models.Workspace
	if err := r.db.Where("system = ?", true).First(&ws).Error; err != nil {
		return nil, err
	}
	return &ws, nil
}

// ExistsByName reports whether a workspace already claims the given handle.
func (r *WorkspaceRepository) ExistsByName(name string) (bool, error) {
	var count int64
	err := r.db.Model(&models.Workspace{}).Where("name = ?", name).Count(&count).Error
	return count > 0, err
}

// FindByName resolves a workspace by its unique handle (the former slug).
func (r *WorkspaceRepository) FindByName(name string) (*models.Workspace, error) {
	var ws models.Workspace
	if err := r.db.Where("name = ?", name).First(&ws).Error; err != nil {
		return nil, err
	}
	return &ws, nil
}

// ListAll returns every workspace (platform-admin view), newest first.
func (r *WorkspaceRepository) ListAll() ([]models.Workspace, error) {
	var workspaces []models.Workspace
	err := r.db.Order("created_at DESC").Find(&workspaces).Error
	return workspaces, err
}

// ListPaged returns workspaces with offset pagination and the total count,
// optionally filtered by a handle/display-name search term. Newest first.
func (r *WorkspaceRepository) ListPaged(search string, limit, offset int) ([]models.Workspace, int64, error) {
	var (
		workspaces []models.Workspace
		total      int64
	)
	q := r.db.Model(&models.Workspace{})
	if s := strings.TrimSpace(search); s != "" {
		like := "%" + strings.ToLower(s) + "%"
		q = q.Where("LOWER(name) LIKE ? OR LOWER(display_name) LIKE ?", like, like)
	}
	q.Count(&total)
	if err := q.Order("created_at DESC").Limit(limit).Offset(offset).Find(&workspaces).Error; err != nil {
		return nil, 0, err
	}
	return workspaces, total, nil
}

// SetPrivileged toggles a workspace's privileged flag (platform-admin only).
func (r *WorkspaceRepository) SetPrivileged(id uint, v bool) error {
	return r.db.Model(&models.Workspace{}).Where("id = ?", id).Update("privileged", v).Error
}

// IsPrivileged reports whether a workspace is privileged, reading only the flag.
// A privileged workspace may expose routes under registered-but-unverified
// domains (the route service's verification gate waives them).
func (r *WorkspaceRepository) IsPrivileged(id uint) (bool, error) {
	var ws models.Workspace
	if err := r.db.Select("privileged").First(&ws, id).Error; err != nil {
		return false, err
	}
	return ws.Privileged, nil
}

// IDsOwnedBy returns the ids of workspaces owned by a user.
func (r *WorkspaceRepository) IDsOwnedBy(userID uint) ([]uint, error) {
	var ids []uint
	err := r.db.Model(&models.Workspace{}).Where("owner_id = ?", userID).Pluck("id", &ids).Error
	return ids, err
}

// ListOwnedBy returns the workspaces a user owns, newest first.
func (r *WorkspaceRepository) ListOwnedBy(userID uint) ([]models.Workspace, error) {
	var workspaces []models.Workspace
	err := r.db.Where("owner_id = ?", userID).Order("created_at DESC").Find(&workspaces).Error
	return workspaces, err
}

// CountOwnedBy returns how many non-system workspaces a user is an owner of
// (by owner-role membership, so promoted co-owners count too). This is the value
// the per-user workspace-count limit is enforced against.
func (r *WorkspaceRepository) CountOwnedBy(userID uint) (int64, error) {
	var count int64
	err := r.db.Model(&models.WorkspaceMember{}).
		Joins("JOIN workspaces ON workspaces.id = workspace_members.workspace_id").
		Where("workspace_members.user_id = ? AND workspace_members.role = ? AND workspaces.system = ?",
			userID, models.WorkspaceRoleOwner, false).
		Count(&count).Error
	return count, err
}

// CountMemberships returns how many workspaces a user belongs to.
func (r *WorkspaceRepository) CountMemberships(userID uint) (int64, error) {
	var count int64
	err := r.db.Model(&models.WorkspaceMember{}).Where("user_id = ?", userID).Count(&count).Error
	return count, err
}

// CountJoinedBy returns how many workspaces a user has JOINED as a non-owner
// member — excluding workspaces they own (counted by CountOwnedBy) and the
// system workspace. This is the count the per-user membership limit caps.
func (r *WorkspaceRepository) CountJoinedBy(userID uint) (int64, error) {
	var count int64
	err := r.db.Model(&models.WorkspaceMember{}).
		Joins("JOIN workspaces ON workspaces.id = workspace_members.workspace_id").
		Where("workspace_members.user_id = ? AND workspace_members.role <> ? AND workspaces.system = ?",
			userID, models.WorkspaceRoleOwner, false).
		Count(&count).Error
	return count, err
}

// CountMembers returns how many members a workspace has.
func (r *WorkspaceRepository) CountMembers(workspaceID uint) (int64, error) {
	var count int64
	err := r.db.Model(&models.WorkspaceMember{}).Where("workspace_id = ?", workspaceID).Count(&count).Error
	return count, err
}

// ListForUser returns workspaces the user is a member of, each annotated with
// the user's role in that workspace.
func (r *WorkspaceRepository) ListForUser(userID uint) ([]models.WorkspaceWithRole, error) {
	var rows []models.WorkspaceWithRole
	err := r.db.
		Model(&models.Workspace{}).
		Select("workspaces.*, workspace_members.role AS role").
		Joins("JOIN workspace_members ON workspace_members.workspace_id = workspaces.id").
		Where("workspace_members.user_id = ?", userID).
		Order("workspaces.created_at DESC").
		Find(&rows).Error
	return rows, err
}

func (r *WorkspaceRepository) AddMember(m *models.WorkspaceMember) error {
	return r.db.Create(m).Error
}

func (r *WorkspaceRepository) FindMember(workspaceID, userID uint) (*models.WorkspaceMember, error) {
	var m models.WorkspaceMember
	if err := r.db.Where("workspace_id = ? AND user_id = ?", workspaceID, userID).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *WorkspaceRepository) ListMembers(workspaceID uint) ([]models.WorkspaceMember, error) {
	var members []models.WorkspaceMember
	err := r.db.Preload("User").Where("workspace_id = ?", workspaceID).
		Order("created_at ASC").Find(&members).Error
	return members, err
}

func (r *WorkspaceRepository) UpdateMemberRole(workspaceID, userID uint, role models.WorkspaceRole) error {
	// A built-in role assignment clears any custom role.
	return r.db.Model(&models.WorkspaceMember{}).
		Where("workspace_id = ? AND user_id = ?", workspaceID, userID).
		Updates(map[string]any{"role": role, "custom_role_id": nil}).Error
}

// SetMemberCustomRole assigns a custom role to a member, recording its base role
// for rank checks.
func (r *WorkspaceRepository) SetMemberCustomRole(workspaceID, userID, customRoleID uint, baseRole models.WorkspaceRole) error {
	return r.db.Model(&models.WorkspaceMember{}).
		Where("workspace_id = ? AND user_id = ?", workspaceID, userID).
		Updates(map[string]any{"role": baseRole, "custom_role_id": customRoleID}).Error
}

func (r *WorkspaceRepository) RemoveMember(workspaceID, userID uint) error {
	return r.db.Where("workspace_id = ? AND user_id = ?", workspaceID, userID).
		Delete(&models.WorkspaceMember{}).Error
}

func (r *WorkspaceRepository) CountOwners(workspaceID uint) (int64, error) {
	var count int64
	err := r.db.Model(&models.WorkspaceMember{}).
		Where("workspace_id = ? AND role = ?", workspaceID, models.WorkspaceRoleOwner).
		Count(&count).Error
	return count, err
}

func (r *WorkspaceRepository) CreateInvitation(inv *models.WorkspaceInvitation) error {
	return r.db.Create(inv).Error
}

// PendingInvitationExists reports whether a pending, unexpired invitation for the
// email already exists in the workspace — used to reject duplicate invites.
func (r *WorkspaceRepository) PendingInvitationExists(workspaceID uint, email string) (bool, error) {
	var count int64
	err := r.db.Model(&models.WorkspaceInvitation{}).
		Where("workspace_id = ? AND email = ? AND status = ? AND expires_at > ?",
			workspaceID, strings.ToLower(strings.TrimSpace(email)), models.InvitationStatusPending, time.Now()).
		Count(&count).Error
	return count > 0, err
}

func (r *WorkspaceRepository) FindInvitationByHash(hash string) (*models.WorkspaceInvitation, error) {
	var inv models.WorkspaceInvitation
	if err := r.db.Where("token_hash = ?", hash).First(&inv).Error; err != nil {
		return nil, err
	}
	return &inv, nil
}

func (r *WorkspaceRepository) UpdateInvitation(inv *models.WorkspaceInvitation) error {
	return r.db.Save(inv).Error
}

func (r *WorkspaceRepository) ListInvitations(workspaceID uint) ([]models.WorkspaceInvitation, error) {
	var invs []models.WorkspaceInvitation
	err := r.db.Where("workspace_id = ? AND status = ?", workspaceID, models.InvitationStatusPending).
		Order("created_at DESC").Find(&invs).Error
	return invs, err
}

// ListInvitationsByEmail returns the pending, unexpired invitations addressed to
// an email — used to surface invitations to the recipient when they log in.
func (r *WorkspaceRepository) ListInvitationsByEmail(email string) ([]models.WorkspaceInvitation, error) {
	var invs []models.WorkspaceInvitation
	err := r.db.Where("email = ? AND status = ? AND expires_at > ?",
		strings.ToLower(strings.TrimSpace(email)), models.InvitationStatusPending, time.Now()).
		Order("created_at DESC").Find(&invs).Error
	return invs, err
}

func (r *WorkspaceRepository) FindInvitationByID(id uint) (*models.WorkspaceInvitation, error) {
	var inv models.WorkspaceInvitation
	if err := r.db.First(&inv, id).Error; err != nil {
		return nil, err
	}
	return &inv, nil
}

// IDByUID resolves a workspace's uid to its numeric id.
func (r *WorkspaceRepository) IDByUID(uid string) (uint, error) {
	return idByUID[models.Workspace](r.db, uid)
}
