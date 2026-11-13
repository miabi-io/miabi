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

	"github.com/miabi-io/miabi/internal/models"
)

// SetMetadata writes the given built-in keys and removes drop, leaving every other key as it is.
func (s *Service) SetMetadata(workspaceID, id uint, set map[string]string, drop ...string) (*models.DatabaseInstance, error) {
	inst, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	m := inst.Metadata
	for _, k := range drop {
		delete(m, k)
	}
	for k, v := range set {
		m = models.SetBuiltin(m, k, v)
	}
	inst.Metadata = m
	if err := s.repo.Update(inst); err != nil {
		return nil, err
	}
	return s.Get(workspaceID, id)
}

// TagDeclared restores the manifest identity a logical database carried, so a restored workspace still converges.
func (s *Service) TagDeclared(d *models.Database, meta models.Metadata) {
	s.tagLogical(d, meta[models.MetaDeclarativeName], meta)
}

// SanitizedName is the logical database name a manifest name becomes on the engine.
func SanitizedName(name string) string { return sanitizeIdent(name) }

// EnsureNamedDatabase never suffixes: a database of that name the manifest did not create is ErrNameTaken.
func (s *Service) EnsureNamedDatabase(ctx context.Context, workspaceID uint, inst *models.DatabaseInstance, declName string, meta models.Metadata) (*models.Database, error) {
	if !inst.SupportsLogicalDatabases() {
		return nil, ErrNoLogicalDBs
	}
	name := sanitizeIdent(declName)
	dbs, err := s.repo.ListDatabases(inst.ID)
	if err != nil {
		return nil, err
	}
	for i := range dbs {
		if dbs[i].Name != name {
			continue
		}
		if dbs[i].Metadata[models.MetaDeclarativeName] == declName {
			return &dbs[i], nil
		}
		return nil, ErrNameTaken
	}
	var db *models.Database
	// An instance declared in the same apply is still coming up, so its database waits for bring-up.
	if inst.Status == models.DBStatusProvisioning {
		db, err = s.PrepareDatabase(workspaceID, inst.ID, name, nil)
	} else {
		db, err = s.CreateDatabase(ctx, workspaceID, inst.ID, name, nil)
	}
	if err != nil {
		return nil, err
	}
	s.tagLogical(db, declName, meta)
	return db, nil
}
