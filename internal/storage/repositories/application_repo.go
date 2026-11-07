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
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

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

// History rows (deployments, releases, events, metrics) have no FK and would otherwise orphan.
func (r *ApplicationRepository) Delete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		app := &models.Application{ID: id}
		if err := tx.Model(app).Association("Networks").Clear(); err != nil {
			return err
		}
		for _, child := range []any{
			&models.AppEnvVar{}, &models.AppPort{},
			&models.Deployment{}, &models.Release{}, &models.AppEvent{}, &models.MetricSample{},
			&models.Job{}, &models.CronJob{}, &models.JobDefinition{},
		} {
			if err := tx.Where("application_id = ?", id).Delete(child).Error; err != nil {
				return err
			}
		}
		policies := tx.Model(&models.VersionPolicy{}).Select("id").Where("application_id = ?", id)
		if err := tx.Where("policy_id IN (?)", policies).Delete(&models.VersionAllocation{}).Error; err != nil {
			return err
		}
		if err := tx.Where("application_id = ?", id).Delete(&models.VersionPolicy{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.Application{}, id).Error
	})
}

// Unscoped by workspace: the deploy worker already holds a trusted deployment.
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

// NamesByID maps workspace application ids to their names, for labelling references in lists.
func (r *ApplicationRepository) NamesByID(workspaceID uint, ids []uint) (map[uint]string, error) {
	out := make(map[uint]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID   uint
		Name string
	}
	if err := r.db.Model(&models.Application{}).Select("id", "name").
		Where("workspace_id = ? AND id IN ?", workspaceID, ids).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ID] = row.Name
	}
	return out, nil
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

func (r *ApplicationRepository) All() ([]models.Application, error) {
	var apps []models.Application
	err := r.db.Order("workspace_id ASC, id ASC").Find(&apps).Error
	return apps, err
}

func (r *ApplicationRepository) ListRunning() ([]models.Application, error) {
	var apps []models.Application
	err := r.db.Where("status = ?", models.AppStatusRunning).Find(&apps).Error
	return apps, err
}

// Failed stays in: the event subscriber marks an app failed when its container is killed on removal.
func (r *ApplicationRepository) ListReconcilable() ([]models.Application, error) {
	var apps []models.Application
	err := r.db.Where("current_release_id IS NOT NULL AND status NOT IN ?",
		[]models.AppStatus{models.AppStatusCreated, models.AppStatusDeploying, models.AppStatusStopped}).
		Find(&apps).Error
	return apps, err
}

// Both columns are JSON, so "no grant" is NULL or one of two empty encodings depending on the writer.
func (r *ApplicationRepository) ListWithGrants() ([]models.Application, error) {
	var apps []models.Application
	empty := []string{"", "[]", "null"}
	err := r.db.
		Where("(add_capabilities IS NOT NULL AND add_capabilities NOT IN ?) OR (devices IS NOT NULL AND devices NOT IN ?)", empty, empty).
		Order("workspace_id ASC, name ASC").
		Find(&apps).Error
	return apps, err
}

func (r *ApplicationRepository) ListByWorkspace(workspaceID uint) ([]models.Application, error) {
	var apps []models.Application
	err := r.db.Where("workspace_id = ?", workspaceID).Order("created_at DESC").Find(&apps).Error
	return apps, err
}

func (r *ApplicationRepository) ListByWorkspaceWithEnv(workspaceID uint) ([]models.Application, error) {
	var apps []models.Application
	err := r.db.Preload("EnvVars").Where("workspace_id = ?", workspaceID).Find(&apps).Error
	return apps, err
}

func (r *ApplicationRepository) ListByServer(serverID uint) ([]models.Application, error) {
	var apps []models.Application
	err := r.db.Preload("Ports").Where("server_id = ?", serverID).Find(&apps).Error
	return apps, err
}

// ListByCluster returns all applications in a cluster (across workspaces), for the cluster's gateway.
func (r *ApplicationRepository) ListByCluster(clusterID uint) ([]models.Application, error) {
	var apps []models.Application
	err := r.db.Preload("Ports").Where("cluster_id = ?", clusterID).Find(&apps).Error
	return apps, err
}

func (r *ApplicationRepository) ExistsByName(workspaceID uint, name string) (bool, error) {
	var count int64
	err := r.db.Model(&models.Application{}).
		Where("workspace_id = ? AND name = ?", workspaceID, name).Count(&count).Error
	return count > 0, err
}

// The label maps to the platform-wide host <label>.<base-domain>, so it's unique across workspaces.
func (r *ApplicationRepository) ExternalLabelTaken(label string, exceptAppID uint) (bool, error) {
	// An empty label is the "unassigned" sentinel, not a claimable host.
	if label == "" {
		return false, nil
	}
	var count int64
	err := r.db.Model(&models.Application{}).
		Where("external_label = ? AND id <> ?", label, exceptAppID).Count(&count).Error
	return count > 0, err
}

// A soft-deleted app reads as absent: the housekeeping orphan condition.
func (r *ApplicationRepository) ExistsByID(id uint) (bool, error) {
	var count int64
	err := r.db.Model(&models.Application{}).Where("id = ?", id).Count(&count).Error
	return count > 0, err
}

// SetRuntimeKind sets only the app's runtime, leaving the rest of the row to concurrent writers.
func (r *ApplicationRepository) SetRuntimeKind(id uint, kind models.RuntimeKind) error {
	return r.db.Model(&models.Application{}).Where("id = ?", id).Update("runtime_kind", kind).Error
}

func (r *ApplicationRepository) SetStatus(id uint, status models.AppStatus) error {
	return r.db.Model(&models.Application{}).Where("id = ?", id).Update("status", status).Error
}

// A targeted UPDATE so concurrent actions can't lose each other; last writer wins, which is right here.
func (r *ApplicationRepository) SetLastLifecycle(id uint, action string, at time.Time, by *uint) error {
	return r.db.Model(&models.Application{}).Where("id = ?", id).Updates(map[string]any{
		"last_lifecycle_at":     at,
		"last_lifecycle_action": action,
		"last_lifecycle_by_id":  by,
	}).Error
}

// Empty when nobody acted or the user was deleted; the action and time still stand.
func (r *ApplicationRepository) LifecycleActorName(appID uint) (string, error) {
	var name string
	err := r.db.Model(&models.Application{}).
		Select("COALESCE(users.name, '')").
		Joins("LEFT JOIN users ON users.id = applications.last_lifecycle_by_id").
		Where("applications.id = ?", appID).
		Scan(&name).Error
	return name, err
}

func (r *ApplicationRepository) SetRedeployRequired(id uint, v bool) error {
	return r.db.Model(&models.Application{}).Where("id = ?", id).Update("redeploy_required", v).Error
}

// Nothing is deleted: the old ref stops being read and registry retention reclaims it.
func (r *ApplicationRepository) BumpCacheGeneration(id uint) error {
	return r.db.Model(&models.Application{}).Where("id = ?", id).
		UpdateColumn("cache_generation", gorm.Expr("cache_generation + 1")).Error
}

func (r *ApplicationRepository) MarkCacheBuilt(id, generation uint) error {
	return r.db.Model(&models.Application{}).
		Where("id = ? AND cache_generation = ?", id, generation).
		UpdateColumn("cache_built_generation", generation).Error
}

// A targeted Update bypasses the serializer (pgx can't encode a Go map to text), so JSON is marshalled here.
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

// Git apps build their own image and use SetCurrentRelease instead.
func (r *ApplicationRepository) SetCurrentReleaseImage(id, releaseID uint, status models.AppStatus, image, tag string) error {
	return r.db.Model(&models.Application{}).Where("id = ?", id).
		Updates(map[string]any{
			"current_release_id": releaseID, "status": status, "redeploy_required": false,
			"image": image, "tag": tag,
		}).Error
}

// Clearing also drops the match rules, so a stale match can never target the next canary; mode is kept.
func (r *ApplicationRepository) SetCanary(id uint, releaseID *uint, weight int) error {
	fields := map[string]any{"canary_release_id": releaseID, "canary_weight": weight}
	if releaseID == nil {
		fields["canary_exclusive"] = false
		fields["canary_priority"] = 0
		fields["canary_match"] = nil
		fields["canary_paused_at"] = nil
	}
	return r.db.Model(&models.Application{}).Where("id = ?", id).Updates(fields).Error
}

func (r *ApplicationRepository) SetCanaryPaused(id uint, at *time.Time) error {
	return r.db.Model(&models.Application{}).Where("id = ?", id).
		Update("canary_paused_at", at).Error
}

// Weight moves only through SetCanary, so saving rules never shifts traffic as a side effect.
func (r *ApplicationRepository) SetCanaryRouting(id uint, mode models.CanaryMode, exclusive bool, priority int, match []models.CanaryMatchRule) error {
	// Select names every column so zero values are written rather than skipped by a struct update.
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

// One query, so showing shadowed stack variables doesn't cost a round trip per app.
func (r *ApplicationRepository) EnvKeyOwners(appIDs []uint) (map[string][]uint, error) {
	out := map[string][]uint{}
	if len(appIDs) == 0 {
		return out, nil
	}
	var rows []models.AppEnvVar
	if err := r.db.Select("application_id", "key").
		Where("application_id IN ?", appIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, v := range rows {
		out[v.Key] = append(out[v.Key], v.ApplicationID)
	}
	return out, nil
}

// IDByUID resolves an application's uid to its numeric id.
func (r *ApplicationRepository) IDByUID(uid string) (uint, error) {
	return idByUID[models.Application](r.db, uid)
}

// Digits are tried as an id first, then as a name, since names may be all digits.
func (r *ApplicationRepository) IDByRef(workspaceID uint, ref string) (uint, error) {
	ref = strings.TrimSpace(ref)
	var id uint
	find := func(where string, arg any) uint {
		var got uint
		r.db.Model(&models.Application{}).Where("workspace_id = ? AND "+where, workspaceID, arg).Limit(1).Pluck("id", &got)
		return got
	}
	if n, err := strconv.ParseUint(ref, 10, 64); err == nil && n > 0 {
		id = find("id = ?", n)
	}
	if id == 0 {
		if _, err := uuid.Parse(ref); err == nil {
			id = find("uid = ?", ref)
		}
	}
	if id == 0 && ref != "" {
		id = find("name = ?", strings.ToLower(ref))
	}
	if id == 0 {
		return 0, gorm.ErrRecordNotFound
	}
	return id, nil
}

// A stopped app must still be able to start, so its image counts as referenced.
func (r *ApplicationRepository) ImageRefs() ([]string, error) {
	var rows []struct{ Image, Tag string }
	if err := r.db.Model(&models.Application{}).
		Select("image", "tag").Where("image <> ''").Scan(&rows).Error; err != nil {
		return nil, err
	}
	refs := make([]string, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, row.Image, models.ComposeImageRef(row.Image, row.Tag))
	}
	return refs, nil
}
