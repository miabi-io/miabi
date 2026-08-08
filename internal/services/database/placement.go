// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
)

// Placement decides how a database dependency is satisfied against the workspace.
// It is shared by the marketplace installer and the declarative (GitOps) apply so
// both honor the same semantics.
type Placement string

const (
	// PlacementAuto reuses a compatible running instance when one exists (creating
	// a dedicated logical database on it), otherwise provisions a fresh instance.
	PlacementAuto Placement = "auto"
	// PlacementDedicated always provisions a fresh instance for the dependency.
	PlacementDedicated Placement = "dedicated"
	// PlacementShared requires an existing running compatible instance and creates
	// a dedicated logical database on it; it never provisions a new instance.
	PlacementShared Placement = "shared"
)

// ErrNoSharedInstance is returned when placement "shared" finds no compatible
// running instance to host the dependency's logical database.
var ErrNoSharedInstance = errors.New("no compatible running database instance to share")

// ResolveDependency satisfies a database dependency per its placement, returning the hosting instance,
// the app-scoped logical database, a connection, and whether a new instance was provisioned. declName
// stamps the logical database so a declarative reconcile maps a manifest name back to it exactly.
func (s *Service) ResolveDependency(ctx context.Context, workspaceID, serverID, pinInstance uint, base, declName string, engine models.DBEngine, version string, placement Placement, meta models.Metadata) (*models.DatabaseInstance, *models.Database, ConnectionInfo, bool, error) {
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
	case !logical, placement == PlacementDedicated:
		return s.provisionDedicated(ctx, workspaceID, base, declName, engine, version, serverID, meta)

	case placement == PlacementShared:
		inst := s.findReusable(workspaceID, engine)
		if inst == nil {
			return nil, nil, ConnectionInfo{}, false, ErrNoSharedInstance
		}
		return s.logicalOn(ctx, workspaceID, inst, base, declName, meta)

	default: // PlacementAuto
		if inst := s.findReusable(workspaceID, engine); inst != nil {
			return s.logicalOn(ctx, workspaceID, inst, base, declName, meta)
		}
		return s.provisionDedicated(ctx, workspaceID, base, declName, engine, version, serverID, meta)
	}
}

// provisionDedicated provisions a fresh instance for a dependency. For a SQL engine it also reserves a logical
// database with its own scoped user, whose CREATE DDL runs when the instance comes up, and returns that
// connection. Redis and libSQL host no logical databases, so the app receives the instance connection.
func (s *Service) provisionDedicated(ctx context.Context, workspaceID uint, base, declName string, engine models.DBEngine, version string, serverID uint, meta models.Metadata) (*models.DatabaseInstance, *models.Database, ConnectionInfo, bool, error) {
	inst, err := s.Provision(ctx, workspaceID, serverID, strings.TrimSpace(base), engine, version, 0, meta, nil)
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
		// Reservation failed (very unlikely on a fresh instance); fall back to the
		// admin connection so the dependency still resolves.
		conn, cErr := s.InstanceConnection(inst)
		return inst, nil, conn, true, cErr
	}
	s.tagLogical(db, declName, meta)
	conn, err := s.DatabaseConnection(inst, db)
	return inst, db, conn, true, err
}

// logicalOn creates a logical database (own scoped user) on an existing running
// instance and returns its scoped connection. The name comes from base,
// uniquified per instance only on collision.
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

// tagLogical stamps a logical database with its declarative name and copies the
// managed-by / gitops-source provenance from meta, so a reconcile can find and
// own it. A no-op when declName is empty (e.g. marketplace installs).
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

// FindReusableInstance returns the running instance a shared/auto placement would
// reuse for engine, or nil. Exposed so a caller can pre-resolve a dependency's
// target node without performing the placement.
func (s *Service) FindReusableInstance(workspaceID uint, engine models.DBEngine) *models.DatabaseInstance {
	return s.findReusable(workspaceID, engine)
}

func (s *Service) findReusable(workspaceID uint, engine models.DBEngine) *models.DatabaseInstance {
	list, err := s.List(workspaceID)
	if err != nil {
		return nil
	}
	for i := range list {
		if list[i].Engine == engine && list[i].Status == models.DBStatusRunning {
			return &list[i]
		}
	}
	return nil
}

// ListDatabasesByWorkspace returns every logical database in the workspace.
func (s *Service) ListDatabasesByWorkspace(workspaceID uint) ([]models.Database, error) {
	return s.repo.ListDatabasesByWorkspace(workspaceID)
}

// FindDatabaseByDeclName returns the logical database stamped with the given
// declarative name and its hosting instance; ok is false when none exists.
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
