// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"encoding/json"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type ApplicationRepository struct {
	db *gorm.DB
}

func NewApplicationRepository(db *gorm.DB) *ApplicationRepository {
	return &ApplicationRepository{db: db}
}

func (r *ApplicationRepository) Create(app *models.Application) error {
	return r.db.Create(app).Error
}

func (r *ApplicationRepository) Update(app *models.Application) error {
	return r.db.Save(app).Error
}

// Delete hard-deletes an application and its child rows in one transaction: the rows holding a
// foreign key to it (env vars, ports, network joins), plus its history rows (deployments,
// releases, events, metric samples), which have no FK and would otherwise orphan.
func (r *ApplicationRepository) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		app := &models.Application{ID: id}
		if err := tx.Model(app).Association("Networks").Clear(); err != nil {
			return err
		}
		for _, child := range []any{
			&models.AppEnvVar{}, &models.AppPort{},
			&models.Deployment{}, &models.Release{}, &models.AppEvent{}, &models.MetricSample{},
		} {
			if err := tx.Where("application_id = ?", id).Delete(child).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&models.Application{}, id).Error
	})
}

// FindByID loads an application by id (with env vars), regardless of workspace.
// Used by the deploy worker, which already holds a trusted deployment.
func (r *ApplicationRepository) FindByID(id uint) (*models.Application, error) {
	var app models.Application
	if err := r.db.Preload("EnvVars").Preload("Networks").Preload("Ports").Preload("Stack").First(&app, id).Error; err != nil {
		return nil, err
	}
	return &app, nil
}

// FindInWorkspace loads an application scoped to a workspace, with env vars.
func (r *ApplicationRepository) FindInWorkspace(workspaceID, id uint) (*models.Application, error) {
	var app models.Application
	if err := r.db.Preload("EnvVars").Preload("Networks").Preload("Ports").Preload("Stack").
		Where("id = ? AND workspace_id = ?", id, workspaceID).First(&app).Error; err != nil {
		return nil, err
	}
	return &app, nil
}

// SetStack assigns (or clears, when stackID is nil) an application's stack.
func (r *ApplicationRepository) SetStack(appID uint, stackID *uint) error {
	return r.db.Model(&models.Application{}).Where("id = ?", appID).
		Update("stack_id", stackID).Error
}

// ReplaceNetworks sets the application's attached networks (many-to-many).
func (r *ApplicationRepository) ReplaceNetworks(app *models.Application, networks []models.Network) error {
	return r.db.Model(app).Association("Networks").Replace(networks)
}

// CountNetworks returns how many networks an application is attached to.
func (r *ApplicationRepository) CountNetworks(appID uint) (int64, error) {
	var count int64
	err := r.db.Table("application_networks").Where("application_id = ?", appID).Count(&count).Error
	return count, err
}

// All returns every application across every workspace, for platform-wide passes —
// disaster recovery reconciles the whole install, not one tenant at a time.
func (r *ApplicationRepository) All() ([]models.Application, error) {
	var apps []models.Application
	err := r.db.Order("workspace_id ASC, id ASC").Find(&apps).Error
	return apps, err
}

// ListRunning returns all applications currently in the running state, across workspaces,
// for the metrics scraper.
func (r *ApplicationRepository) ListRunning() ([]models.Application, error) {
	var apps []models.Application
	err := r.db.Where("status = ?", models.AppStatusRunning).Find(&apps).Error
	return apps, err
}

func (r *ApplicationRepository) ListByWorkspace(workspaceID uint) ([]models.Application, error) {
	var apps []models.Application
	err := r.db.Where("workspace_id = ?", workspaceID).Order("created_at DESC").Find(&apps).Error
	return apps, err
}

// ListByWorkspaceWithEnv lists workspace apps with their env vars preloaded, for
// scanning secret references.
func (r *ApplicationRepository) ListByWorkspaceWithEnv(workspaceID uint) ([]models.Application, error) {
	var apps []models.Application
	err := r.db.Preload("EnvVars").Where("workspace_id = ?", workspaceID).Find(&apps).Error
	return apps, err
}

// ListByServer returns all applications placed on a node (across workspaces),
// for rendering that node's Goma routes.
func (r *ApplicationRepository) ListByServer(serverID uint) ([]models.Application, error) {
	var apps []models.Application
	err := r.db.Preload("Ports").Where("server_id = ?", serverID).Find(&apps).Error
	return apps, err
}

func (r *ApplicationRepository) ExistsByName(workspaceID uint, name string) (bool, error) {
	var count int64
	err := r.db.Model(&models.Application{}).
		Where("workspace_id = ? AND name = ?", workspaceID, name).Count(&count).Error
	return count > 0, err
}

// ExternalLabelTaken reports whether a non-deleted application other than exceptAppID already
// owns this external-access label. The label maps to a platform-wide host,
// `<label>.<base-domain>`, so it must be unique across all workspaces.
func (r *ApplicationRepository) ExternalLabelTaken(label string, exceptAppID uint) (bool, error) {
	// An empty label is the "unassigned" sentinel, not a claimable host, so it is
	// never taken (and must not collide with other unlabeled apps).
	if label == "" {
		return false, nil
	}
	var count int64
	err := r.db.Model(&models.Application{}).
		Where("external_label = ? AND id <> ?", label, exceptAppID).Count(&count).Error
	return count > 0, err
}

// ExistsByID reports whether a non-deleted application exists. A soft-deleted
// app reads as absent — the housekeeping "orphan" condition (deleted in Miabi
// but whose containers still run).
func (r *ApplicationRepository) ExistsByID(id uint) (bool, error) {
	var count int64
	err := r.db.Model(&models.Application{}).Where("id = ?", id).Count(&count).Error
	return count > 0, err
}

func (r *ApplicationRepository) SetStatus(id uint, status models.AppStatus) error {
	return r.db.Model(&models.Application{}).Where("id = ?", id).Update("status", status).Error
}

func (r *ApplicationRepository) SetRedeployRequired(id uint, v bool) error {
	return r.db.Model(&models.Application{}).Where("id = ?", id).Update("redeploy_required", v).Error
}

// BumpCacheGeneration invalidates an app's build cache by naming a new one. Nothing is deleted:
// the old ref simply stops being read, and the registry's retention reclaims it.
func (r *ApplicationRepository) BumpCacheGeneration(id uint) error {
	return r.db.Model(&models.Application{}).Where("id = ?", id).
		UpdateColumn("cache_generation", gorm.Expr("cache_generation + 1")).Error
}

func (r *ApplicationRepository) MarkCacheBuilt(id, generation uint) error {
	return r.db.Model(&models.Application{}).
		Where("id = ? AND cache_generation = ?", id, generation).
		UpdateColumn("cache_built_generation", generation).Error
}

// SetContainerLabels replaces an app's user-defined Docker labels. A targeted Update bypasses
// the field serializer (pgx can't encode a Go map into the text column), so the JSON is
// marshalled here to match what the serializer writes. nil/empty clears them (stored NULL).
func (r *ApplicationRepository) SetContainerLabels(id uint, labels map[string]string) error {
	var val any
	if len(labels) > 0 {
		b, err := json.Marshal(labels)
		if err != nil {
			return err
		}
		val = string(b)
	}
	return r.db.Model(&models.Application{}).Where("id = ?", id).Update("container_labels", val).Error
}

func (r *ApplicationRepository) SetCurrentRelease(id, releaseID uint, status models.AppStatus) error {
	// A successful deploy applies the latest config, so clear the pending flag.
	return r.db.Model(&models.Application{}).Where("id = ?", id).
		Updates(map[string]any{"current_release_id": releaseID, "status": status, "redeploy_required": false}).Error
}

// SetCurrentReleaseImage is SetCurrentRelease plus persisting the deployed
// image/tag, so the app's stored Image/Tag reflect the now-active version (used
// by image-source apps; git apps build their own image and use SetCurrentRelease).
func (r *ApplicationRepository) SetCurrentReleaseImage(id, releaseID uint, status models.AppStatus, image, tag string) error {
	return r.db.Model(&models.Application{}).Where("id = ?", id).
		Updates(map[string]any{
			"current_release_id": releaseID, "status": status, "redeploy_required": false,
			"image": image, "tag": tag,
		}).Error
}

// SetCanary records (or clears) the in-progress canary release and its traffic
// weight. Pass releaseID=nil and weight=0 to clear the canary.
//
// Clearing also drops the rollout's routing rules. Every promote/abort/supersede
// path goes through here, so doing it in one place is what guarantees a stale
// `match` can never survive to target the next canary. The mode is a user
// preference and is deliberately kept.
func (r *ApplicationRepository) SetCanary(id uint, releaseID *uint, weight int) error {
	fields := map[string]any{"canary_release_id": releaseID, "canary_weight": weight}
	if releaseID == nil {
		fields["canary_exclusive"] = false
		fields["canary_priority"] = 0
		fields["canary_match"] = nil
	}
	return r.db.Model(&models.Application{}).Where("id = ?", id).Updates(fields).Error
}

// SetCanaryRouting stores the app's canary steering: the mode, and (for manual
// mode) the match rules the gateway routes on. Weight is untouched — it moves
// through SetCanary — so saving rules never shifts traffic as a side effect.
func (r *ApplicationRepository) SetCanaryRouting(id uint, mode models.CanaryMode, exclusive bool, priority int, match []models.CanaryMatchRule) error {
	// Select names every column so the zero values (no rules, not exclusive,
	// priority 0) are written rather than skipped as a struct update's defaults.
	return r.db.Model(&models.Application{}).Where("id = ?", id).
		Select("canary_mode", "canary_exclusive", "canary_priority", "canary_match").
		Updates(models.Application{
			CanaryMode:      mode,
			CanaryExclusive: exclusive,
			CanaryPriority:  priority,
			CanaryMatch:     match,
		}).Error
}

func (r *ApplicationRepository) UpsertEnvVar(v *models.AppEnvVar) error {
	var existing models.AppEnvVar
	err := r.db.Where("application_id = ? AND key = ?", v.ApplicationID, v.Key).First(&existing).Error
	if err == nil {
		existing.Value = v.Value
		existing.IsSecret = v.IsSecret
		return r.db.Save(&existing).Error
	}
	return r.db.Create(v).Error
}

func (r *ApplicationRepository) DeleteEnvVar(appID uint, key string) error {
	return r.db.Where("application_id = ? AND key = ?", appID, key).Delete(&models.AppEnvVar{}).Error
}

func (r *ApplicationRepository) ListEnvVars(appID uint) ([]models.AppEnvVar, error) {
	var vars []models.AppEnvVar
	err := r.db.Where("application_id = ?", appID).Order("key ASC").Find(&vars).Error
	return vars, err
}

// IDByUID resolves an application's uid to its numeric id.
func (r *ApplicationRepository) IDByUID(uid string) (uint, error) {
	return idByUID[models.Application](r.db, uid)
}
