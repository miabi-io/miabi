// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package repositories

import (
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

type RouteRepository struct {
	db *gorm.DB
}

func NewRouteRepository(db *gorm.DB) *RouteRepository { return &RouteRepository{db: db} }

func (r *RouteRepository) Create(rt *models.Route) error { return r.db.Create(rt).Error }
func (r *RouteRepository) Update(rt *models.Route) error { return r.db.Save(rt).Error }

// UpdateStatus persists a route's config-sync status without touching the rest of
// the row (so a reconcile never clobbers a concurrent edit). syncedAt is the
// reconcile time; a zero value leaves synced_at unchanged.
func (r *RouteRepository) UpdateStatus(id uint, status models.RouteStatus, reason string, syncedAt time.Time) error {
	fields := map[string]any{"status": status, "status_reason": reason}
	if !syncedAt.IsZero() {
		fields["synced_at"] = syncedAt
	}
	return r.db.Model(&models.Route{}).Where("id = ?", id).Updates(fields).Error
}
func (r *RouteRepository) Delete(id uint) error { return r.db.Delete(&models.Route{}, id).Error }

func (r *RouteRepository) FindInWorkspace(workspaceID, id uint) (*models.Route, error) {
	var rt models.Route
	if err := r.db.Where("id = ? AND workspace_id = ?", id, workspaceID).First(&rt).Error; err != nil {
		return nil, err
	}
	return &rt, nil
}

func (r *RouteRepository) ListByWorkspace(workspaceID uint) ([]models.Route, error) {
	var routes []models.Route
	err := r.db.Where("workspace_id = ?", workspaceID).Order("created_at DESC").Find(&routes).Error
	return routes, err
}

// ListAll returns every route across all workspaces. Used to enforce globally
// unique route hostnames (a host maps to exactly one route, regardless of
// workspace). Hosts are JSON-serialized, so membership is checked in Go.
func (r *RouteRepository) ListAll() ([]models.Route, error) {
	var routes []models.Route
	err := r.db.Find(&routes).Error
	return routes, err
}

// ListPagedAll returns a page of routes across every workspace, for the platform admin Routes
// view. search matches the name or any host (hosts is JSON text, so a substring match suffices);
// status filters by config-sync status; a non-nil workspaceID scopes to one workspace.
func (r *RouteRepository) ListPagedAll(search, status string, workspaceID *uint, limit, offset int) ([]models.Route, int64, error) {
	q := r.db.Model(&models.Route{})
	if s := strings.TrimSpace(search); s != "" {
		like := "%" + strings.ToLower(s) + "%"
		q = q.Where("LOWER(name) LIKE ? OR LOWER(hosts) LIKE ?", like, like)
	}
	if st := strings.TrimSpace(status); st != "" {
		q = q.Where("status = ?", st)
	}
	if workspaceID != nil {
		q = q.Where("workspace_id = ?", *workspaceID)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []models.Route
	err := q.Order("created_at DESC").Limit(limit).Offset(offset).Find(&out).Error
	return out, total, err
}

// CountByStatus returns the number of routes in each config-sync status across
// all workspaces, for the admin resync summary and Routes overview.
func (r *RouteRepository) CountByStatus() (map[models.RouteStatus]int64, error) {
	var rows []struct {
		Status models.RouteStatus
		N      int64
	}
	if err := r.db.Model(&models.Route{}).Select("status, COUNT(*) AS n").Group("status").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[models.RouteStatus]int64, len(rows))
	for _, row := range rows {
		out[row.Status] = row.N
	}
	return out, nil
}

func (r *RouteRepository) ListByApp(appID uint) ([]models.Route, error) {
	var routes []models.Route
	err := r.db.Where("application_id = ?", appID).Order("created_at DESC").Find(&routes).Error
	return routes, err
}

func (r *RouteRepository) ListByCertificate(workspaceID, certID uint) ([]models.Route, error) {
	var routes []models.Route
	err := r.db.Where("workspace_id = ? AND certificate_id = ?", workspaceID, certID).
		Order("name ASC").Find(&routes).Error
	return routes, err
}

func (r *RouteRepository) ExistsByName(workspaceID uint, name string) (bool, error) {
	var count int64
	err := r.db.Model(&models.Route{}).
		Where("workspace_id = ? AND name = ?", workspaceID, name).Count(&count).Error
	return count > 0, err
}

func (r *RouteRepository) CountByApp(appID uint) (int64, error) {
	var count int64
	err := r.db.Model(&models.Route{}).Where("application_id = ?", appID).Count(&count).Error
	return count, err
}

// IDByUID resolves a route's uid to its numeric id.
func (r *RouteRepository) IDByUID(uid string) (uint, error) { return idByUID[models.Route](r.db, uid) }
