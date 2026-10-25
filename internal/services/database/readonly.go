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
	"fmt"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

// ErrReadOnlyUnsupported refuses a read-only user on an engine that has none here.
var ErrReadOnlyUnsupported = errors.New("read-only users are available for PostgreSQL databases only")

// EnableReadOnly gives a logical database a read-only user: SELECT on every table, now and future. The
// credentials are generated once; a database still provisioning gets them when its instance comes up,
// a running one at once. Calling it again re-applies the grants (after a restore that stripped them).
func (s *Service) EnableReadOnly(ctx context.Context, d *models.Database) error {
	inst, err := s.repo.FindByID(d.InstanceID)
	if err != nil {
		return ErrNotFound
	}
	if inst.Engine != models.DBEnginePostgres {
		return ErrReadOnlyUnsupported
	}
	if d.ReadOnlyUsername == "" {
		enc, err := crypto.EncryptWS(d.WorkspaceID, token(16))
		if err != nil {
			return err
		}
		d.ReadOnlyUsername, d.ReadOnlyPasswordEnc = "r_"+token(4), enc
		if err := s.repo.UpdateDatabase(d); err != nil {
			return err
		}
	}
	if inst.Status != models.DBStatusRunning || d.Status != models.DBStatusRunning {
		return nil // applyPendingDatabases creates it with the database
	}
	return s.applyReadOnly(ctx, inst, d)
}

// DisableReadOnly removes a database's read-only user and its privileges.
func (s *Service) DisableReadOnly(ctx context.Context, d *models.Database) error {
	if d.ReadOnlyUsername == "" {
		return nil
	}
	inst, err := s.repo.FindByID(d.InstanceID)
	if err != nil {
		return ErrNotFound
	}
	if inst.Status == models.DBStatusRunning && d.Status == models.DBStatusRunning {
		if err := s.execDDLIn(ctx, inst, d.Name, readOnlyRevokeDDL(d.ReadOnlyUsername)); err != nil {
			return fmt.Errorf("revoke read-only user: %w", err)
		}
		if err := s.execDDL(ctx, inst, readOnlyDropDDL(d.Name, d.ReadOnlyUsername)); err != nil {
			return fmt.Errorf("drop read-only user: %w", err)
		}
	}
	d.ReadOnlyUsername, d.ReadOnlyPasswordEnc = "", ""
	return s.repo.UpdateDatabase(d)
}

// applyReadOnly creates (or re-passwords) the read-only user and grants it read access inside the
// database. A no-op for a database without one.
func (s *Service) applyReadOnly(ctx context.Context, inst *models.DatabaseInstance, d *models.Database) error {
	if d.ReadOnlyUsername == "" || inst.Engine != models.DBEnginePostgres {
		return nil
	}
	pass, err := crypto.Decrypt(d.ReadOnlyPasswordEnc)
	if err != nil {
		return err
	}
	if err := s.execDDL(ctx, inst, readOnlyServerDDL(d.Name, d.ReadOnlyUsername, pass)); err != nil {
		return fmt.Errorf("create read-only user: %w", err)
	}
	if err := s.execDDLIn(ctx, inst, d.Name, readOnlyGrantDDL(d.Username, d.ReadOnlyUsername)); err != nil {
		return fmt.Errorf("grant read-only user: %w", err)
	}
	return nil
}

// dropReadOnlyRole removes the read-only role once its database is gone (its grants went with it).
func (s *Service) dropReadOnlyRole(ctx context.Context, inst *models.DatabaseInstance, d *models.Database) {
	if d.ReadOnlyUsername == "" || inst.Engine != models.DBEnginePostgres {
		return
	}
	if err := s.execDDL(ctx, inst, []string{fmt.Sprintf(`DROP ROLE IF EXISTS "%s"`, d.ReadOnlyUsername)}); err != nil {
		logger.Error("drop read-only role", "database", d.ID, "error", err)
	}
}
