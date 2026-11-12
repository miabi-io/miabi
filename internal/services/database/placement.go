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

package database

import (
	"context"
	"errors"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
)

// Placement decides how a database dependency is satisfied; shared by the marketplace installer and GitOps apply.
type Placement string

const (
	PlacementAuto Placement = "auto"
	// PlacementDedicated always provisions a fresh instance for the dependency.
	PlacementDedicated Placement = "dedicated"
	// Provisions only when every running instance is on another version: a database never silently runs a version it didn't ask for.
	PlacementShared Placement = "shared"
)

var ErrNoSharedInstance = errors.New("no compatible running database instance to share")

var ErrInstanceDedicated = errors.New("this instance is dedicated to a single dependency and does not host other databases")

// declName stamps the logical database so a declarative reconcile maps a manifest name back to it exactly.
func (s *Service) ResolveDependency(ctx context.Context, workspaceID, serverID, pinInstance uint, base, declName string, engine models.DBEngine, version string, placement Placement, res Resources, meta models.Metadata) (*models.DatabaseInstance, *models.Database, ConnectionInfo, bool, error) {
	logical := models.EngineSupportsLogicalDatabases(engine)

	// Explicit instance pin → logical database on it (when the engine has them).
	if pinInstance != 0 && logical {
		inst, err := s.Get(workspaceID, pinInstance)
		if err != nil {
			return nil, nil, ConnectionInfo{}, false, err
		}
		return s.logicalOn(ctx, workspaceID, inst, base, declName, meta)
	}

	switch {
	// A sized dependency needs an instance of its own: a reused one already runs with another's limits.
	case !logical, placement == PlacementDedicated, placement == PlacementAuto && res != (Resources{}):
		return s.provisionDedicated(ctx, workspaceID, base, declName, engine, version, serverID, res, dedicatedMeta(meta))

	case placement == PlacementShared:
		if inst := s.findReusable(workspaceID, engine, version, serverID); inst != nil {
			return s.logicalOn(ctx, workspaceID, inst, base, declName, meta)
		}
		if s.findReusable(workspaceID, engine, "", serverID) == nil {
			return nil, nil, ConnectionInfo{}, false, ErrNoSharedInstance
		}
		return s.provisionDedicated(ctx, workspaceID, base, declName, engine, version, serverID, res, meta)

	default: // PlacementAuto
		if inst := s.findReusable(workspaceID, engine, version, serverID); inst != nil {
			return s.logicalOn(ctx, workspaceID, inst, base, declName, meta)
		}
		return s.provisionDedicated(ctx, workspaceID, base, declName, engine, version, serverID, res, meta)
	}
}

// dedicatedMeta copies meta because callers reuse it to tag the logical database.
func dedicatedMeta(meta models.Metadata) models.Metadata {
	out := make(models.Metadata, len(meta)+1)
	for k, v := range meta {
		out[k] = v
	}
	return models.SetBuiltin(out, models.MetaPlacement, models.PlacementValueDedicated)
}

// IsDedicated reports whether inst was provisioned for a single dependency and must not host others.
func IsDedicated(inst *models.DatabaseInstance) bool {
	return inst.Metadata[models.MetaPlacement] == models.PlacementValueDedicated
}

func (s *Service) SetDedicated(workspaceID, id uint, dedicated bool) (*models.DatabaseInstance, error) {
	inst, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	if dedicated {
		inst.Metadata = models.SetBuiltin(inst.Metadata, models.MetaPlacement, models.PlacementValueDedicated)
	} else {
		delete(inst.Metadata, models.MetaPlacement)
	}
	if err := s.repo.Update(inst); err != nil {
		return nil, err
	}
	return s.Get(workspaceID, id)
}

// CheckDedicatedAttach refuses attaching a database on a dedicated instance to an app other than the one it serves.
func (s *Service) CheckDedicatedAttach(inst *models.DatabaseInstance, appID uint) error {
	if !IsDedicated(inst) {
		return nil
	}
	dbs, err := s.repo.ListDatabases(inst.ID)
	if err != nil {
		return err
	}
	for _, d := range dbs {
		if d.ApplicationID != nil && *d.ApplicationID != appID {
			return ErrInstanceDedicated
		}
	}
	return nil
}

// Redis and libSQL host no logical databases, so the app receives the instance connection.
func (s *Service) provisionDedicated(ctx context.Context, workspaceID uint, base, declName string, engine models.DBEngine, version string, serverID uint, res Resources, meta models.Metadata) (*models.DatabaseInstance, *models.Database, ConnectionInfo, bool, error) {
	inst, err := s.Provision(ctx, workspaceID, serverID, strings.TrimSpace(base), engine, version, 0, "", res, meta, nil)
	if err != nil {
		return nil, nil, ConnectionInfo{}, false, err
	}
	if engine == models.DBEngineRedis || engine == models.DBEngineLibSQL {
		conn, cErr := s.InstanceConnection(inst)
		return inst, nil, conn, true, cErr
	}
	name, err := s.UniqueDatabaseName(inst.ID, base)
	if err != nil {
		return nil, nil, ConnectionInfo{}, false, err
	}
	db, err := s.PrepareDatabase(workspaceID, inst.ID, name, nil)
	if err != nil {
		// Fall back to the admin connection so the dependency still resolves.
		conn, cErr := s.InstanceConnection(inst)
		return inst, nil, conn, true, cErr
	}
	s.tagLogical(db, declName, meta)
	conn, err := s.DatabaseConnection(inst, db)
	return inst, db, conn, true, err
}

func (s *Service) logicalOn(ctx context.Context, workspaceID uint, inst *models.DatabaseInstance, base, declName string, meta models.Metadata) (*models.DatabaseInstance, *models.Database, ConnectionInfo, bool, error) {
	name, err := s.UniqueDatabaseName(inst.ID, base)
	if err != nil {
		return nil, nil, ConnectionInfo{}, false, err
	}
	db, err := s.CreateDatabase(ctx, workspaceID, inst.ID, name, nil)
	if err != nil {
		return nil, nil, ConnectionInfo{}, false, err
	}
	s.tagLogical(db, declName, meta)
	conn, err := s.DatabaseConnection(inst, db)
	return inst, db, conn, false, err
}

// No-op when declName is empty (e.g. marketplace installs).
func (s *Service) tagLogical(d *models.Database, declName string, meta models.Metadata) {
	if d == nil || declName == "" {
		return
	}
	m := d.Metadata
	changed := false
	if m[models.MetaDeclarativeName] != declName {
		m = models.SetBuiltin(m, models.MetaDeclarativeName, declName)
		changed = true
	}
	for _, k := range []string{models.MetaManagedBy, models.MetaGitOpsSource} {
		if v := meta[k]; v != "" && m[k] != v {
			m = models.SetBuiltin(m, k, v)
			changed = true
		}
	}
	if changed {
		d.Metadata = m
		if err := s.repo.UpdateDatabase(d); err != nil {
			logger.Warn("tag logical database declarative name", "database", d.ID, "error", err)
		}
	}
}

// FindReusableInstance lets a caller pre-resolve a dependency's target node; a non-zero clusterID scopes the search.
func (s *Service) FindReusableInstance(workspaceID uint, engine models.DBEngine, version string, clusterID uint) *models.DatabaseInstance {
	return s.reusableIn(workspaceID, engine, version, clusterID, clusterID != 0)
}

// Scoped to serverID's location because private networks don't span locations; an empty version matches any.
func (s *Service) findReusable(workspaceID uint, engine models.DBEngine, version string, serverID uint) *models.DatabaseInstance {
	cluster, scoped := s.clusterOfServer(serverID)
	return s.reusableIn(workspaceID, engine, version, cluster, scoped)
}

func (s *Service) reusableIn(workspaceID uint, engine models.DBEngine, version string, cluster uint, scoped bool) *models.DatabaseInstance {
	list, err := s.List(workspaceID)
	if err != nil {
		return nil
	}
	version = strings.TrimSpace(version)
	for i := range list {
		if list[i].Engine != engine || list[i].Status != models.DBStatusRunning || IsDedicated(&list[i]) {
			continue
		}
		if version != "" && list[i].Version != version {
			continue
		}
		if scoped && list[i].ClusterID != cluster {
			continue
		}
		return &list[i]
	}
	return nil
}

func (s *Service) clusterOfServer(serverID uint) (uint, bool) {
	if serverID == 0 || s.serverInfo == nil {
		return 0, false
	}
	srv, err := s.serverInfo.Get(serverID)
	if err != nil || srv == nil {
		return 0, false
	}
	return srv.ClusterID, true
}

// ListDatabasesByWorkspace returns every logical database in the workspace.
func (s *Service) ListDatabasesByWorkspace(workspaceID uint) ([]models.Database, error) {
	return s.repo.ListDatabasesByWorkspace(workspaceID)
}

func (s *Service) FindDatabaseByDeclName(workspaceID uint, declName string) (*models.Database, *models.DatabaseInstance, bool) {
	if declName == "" {
		return nil, nil, false
	}
	dbs, err := s.repo.ListDatabasesByWorkspace(workspaceID)
	if err != nil {
		return nil, nil, false
	}
	for i := range dbs {
		if dbs[i].Metadata[models.MetaDeclarativeName] != declName {
			continue
		}
		inst, err := s.repo.FindInWorkspace(workspaceID, dbs[i].InstanceID)
		if err != nil {
			return nil, nil, false
		}
		return &dbs[i], inst, true
	}
	return nil, nil, false
}
