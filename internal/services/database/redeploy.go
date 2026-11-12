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

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

// ErrNodeGone refuses a redeploy whose node was removed: the data volume lived there.
var ErrNodeGone = errors.New("the instance's node was removed; move or restore the instance instead")

// Redeploy recreates the instance's container from its stored record on the same data volume, in the background.
func (s *Service) Redeploy(ctx context.Context, inst *models.DatabaseInstance) error {
	if inst.Status == models.DBStatusUpgrading {
		return ErrUpgradeInProgress
	}
	if _, ok := specs[inst.Engine]; !ok {
		return ErrUnsupportedEngine
	}
	if s.nodeGone(inst) {
		return ErrNodeGone
	}
	if _, err := s.dockerFor(inst); err != nil {
		return err
	}
	// Refuse up front: bringUp would otherwise create an empty volume under the lost one's name.
	if err := s.guardData(ctx, inst); err != nil {
		return err
	}
	if err := s.enqueuer.EnqueueRedeployDB(inst.ID, inst.ServerID); err != nil {
		return err
	}
	inst.Status = models.DBStatusProvisioning
	if err := s.repo.Update(inst); err != nil {
		return err
	}
	s.publishProgress(inst, "Redeploy queued")
	return nil
}

func (s *Service) RunRedeploy(ctx context.Context, instanceID uint) error {
	inst, err := s.repo.FindByID(instanceID)
	if err != nil {
		return err
	}
	spec, ok := specs[inst.Engine]
	if !ok {
		return ErrUnsupportedEngine
	}
	fail := func(cause error) error {
		inst.Status = models.DBStatusFailed
		_ = s.repo.Update(inst)
		s.publishStatus(inst)
		s.emit(inst, models.EventDatabaseProvisionFailed, models.SeverityError, "Redeploy failed: "+cause.Error(), nil)
		return cause
	}
	adminPass, err := crypto.Decrypt(inst.AdminPasswordEnc)
	if err != nil {
		return fail(err)
	}
	if err := s.guardData(ctx, inst); err != nil {
		return fail(err)
	}
	s.publishProgress(inst, "Recreating container")
	if err := s.bringUp(ctx, inst, spec, adminPass); err != nil {
		logger.Warn("database redeploy failed", "id", inst.ID, "error", err)
		return fail(err)
	}
	if inst.Engine == models.DBEngineLibSQL {
		s.markLibsqlDatabaseRunning(ctx, inst)
	} else {
		s.applyPendingDatabases(ctx, inst)
	}
	s.publishStatus(inst)
	s.emit(inst, models.EventDatabaseRestarted, models.SeverityInfo, "Redeployed with "+describeResources(inst), nil)
	return nil
}

// containerLost reports whether a lifecycle call failed only because the recorded container no longer exists.
func containerLost(inst *models.DatabaseInstance, err error) bool {
	return inst.ContainerID == "" || errors.Is(err, docker.ErrNotFound)
}
