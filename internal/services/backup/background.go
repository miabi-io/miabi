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

package backup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/restores"
)

// A manual run outlives its request, since a large dump takes longer than any HTTP timeout; this bounds it instead.
const backgroundTimeout = 24 * time.Hour

// A row silent this long belongs to a process that died, not to a slow dump.
const staleAfter = restores.StaleAfter

var (
	ErrAlreadyRunning    = errors.New("a backup of this database is already running")
	ErrSetAlreadyRunning = errors.New("a recovery point of this instance is already running")
)

// RunInBackground returns once the backup's row exists; the run continues detached and reports to opts.RequestedBy.
func (s *Service) RunInBackground(inst *models.DatabaseInstance, db *models.Database, opts RunOptions, dest Destination) (*models.Backup, error) {
	if running, err := s.repo.Running(db.ID, time.Now().Add(-staleAfter)); err != nil {
		return nil, err
	} else if running {
		return nil, ErrAlreadyRunning
	}
	started := make(chan *models.Backup, 1)
	opts.started = started
	return detach(started, func(ctx context.Context) error {
		_, err := s.Run(ctx, inst, db, opts, dest)
		return err
	})
}

// RunSetInBackground is RunInBackground for an instance's recovery point.
func (s *Service) RunSetInBackground(inst *models.DatabaseInstance, opts SetOptions, dest Destination) (*models.DatabaseBackupSet, error) {
	if s.sets == nil {
		return nil, ErrSetsUnavailable
	}
	if running, err := s.sets.Running(inst.ID, time.Now().Add(-staleAfter)); err != nil {
		return nil, err
	} else if running {
		return nil, ErrSetAlreadyRunning
	}
	started := make(chan *models.DatabaseBackupSet, 1)
	opts.started = started
	return detach(started, func(ctx context.Context) error {
		_, err := s.RunSet(ctx, inst, opts, dest)
		return err
	})
}

// Errors before the row exists come back to the caller; after it, the run records them on the row.
func detach[T any](started <-chan *T, run func(ctx context.Context) error) (*T, error) {
	failed := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("background backup panicked", "panic", r)
				failed <- fmt.Errorf("backup crashed: %v", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), backgroundTimeout)
		defer cancel()
		if err := run(ctx); err != nil {
			failed <- err
		}
	}()
	select {
	case row := <-started:
		return row, nil
	case err := <-failed:
		return nil, err
	}
}

// FailStale closes runs a restart abandoned, so they don't stay "running" and block the next manual run.
func (s *Service) FailStale() error {
	cutoff := time.Now().Add(-staleAfter)
	const reason = "interrupted: Miabi restarted while this backup ran"
	if n, err := s.repo.FailStale(cutoff, reason); err != nil {
		return err
	} else if n > 0 {
		logger.Warn("closed abandoned backups", "count", n)
	}
	if s.sets == nil {
		return nil
	}
	if n, err := s.sets.FailStale(cutoff, reason); err != nil {
		return err
	} else if n > 0 {
		logger.Warn("closed abandoned recovery points", "count", n)
	}
	return nil
}
