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

package platformbackup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
)

// Most failures are transient, and retries are safe because every run writes a new timestamped artifact.
const (
	maxAttempts  = 3
	retryBackoff = 5 * time.Second
)

// runHelper returns the last attempt's output so the log matches the recorded outcome.
func (s *Service) runHelper(ctx context.Context, dc docker.Client, action string, spec docker.RunSpec) (out string, err error) {
	baseName := spec.Name
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Docker refuses duplicate names, and a previous attempt's container may still be going away.
		if attempt > 1 {
			spec.Name = fmt.Sprintf("%s-r%d", baseName, attempt)
		}

		exit, output, runErr := dc.RunOneShot(ctx, spec)
		if runErr == nil && exit == 0 {
			return output, nil
		}
		err = oneShotError(action, exit, output, runErr)
		out = output

		if attempt == maxAttempts || !worthRetrying(output, runErr) {
			return out, err
		}
		logger.Warn("platform backup helper failed; retrying",
			"action", action, "attempt", attempt, "of", maxAttempts, "error", err)

		select {
		case <-ctx.Done():
			return out, errors.Join(err, ctx.Err())
		case <-time.After(time.Duration(attempt) * retryBackoff):
		}
	}
	return out, err
}

// These fail identically every attempt; retrying only buries the cause under repeated output.
var permanentFailures = []string{
	"access denied",
	"invalid access key",
	"signaturedoesnotmatch",
	"no such bucket",
	"nosuchbucket",
	"does not exist",
	"permission denied",
	"authentication failed",
	"password authentication",
	"unknown flag",
	"no such file or directory",
}

// Defaults to yes: retrying a permanent failure costs seconds, skipping a transient one costs the recovery point.
func worthRetrying(out string, err error) bool {
	if err != nil {
		return true // a Docker-level failure (image pull, daemon blip) is usually transient
	}
	lower := strings.ToLower(out)
	for _, marker := range permanentFailures {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}

// RetrySet re-runs only failed items in place; successful, often expensive artifacts are kept.
func (s *Service) RetrySet(ctx context.Context, set *models.PlatformBackupSet) (*models.PlatformBackupSet, error) {
	// As in CreateSet: retried artifacts must not be cancelled by the triggering request.
	ctx = context.WithoutCancel(ctx)

	st, err := s.getSettings()
	if err != nil {
		return nil, err
	}
	if _, err := s.s3Config(st); err != nil {
		return nil, err
	}

	failed := make([]*models.PlatformBackup, 0, len(set.Items))
	for i := range set.Items {
		if set.Items[i].Status == models.BackupFailed {
			failed = append(failed, &set.Items[i])
		}
	}
	if len(failed) == 0 {
		return nil, ErrNothingToRetry
	}

	// Reopen first: finalizeSet ignores terminal sets, so a retry would otherwise never close it.
	set.Status = models.BackupRunning
	set.Error = ""
	set.FinishedAt = nil
	if err := s.sets.Update(set); err != nil {
		return nil, err
	}

	logger.Info("retrying failed artifacts of a recovery point", "ref", set.Ref, "items", len(failed))
	for _, item := range failed {
		item.Status = models.BackupPending
		item.Error = ""
		item.Filename = ""
		item.StartedAt, item.FinishedAt = nil, nil
		if err := s.repo.Update(item); err != nil {
			return nil, err
		}

		// The identity envelope is sealed in-process; the rest go through the worker.
		if item.Subject == models.PlatformBackupIdentity {
			if err := s.runIdentityBackup(ctx, item, st); err != nil {
				return nil, err
			}
			continue
		}
		if s.enqueuer == nil {
			if err := s.RunBackup(ctx, item.ID); err != nil {
				return nil, err
			}
			continue
		}
		if err := s.enqueuer.EnqueuePlatformBackup(item.ID); err != nil {
			s.fail(item, fmt.Errorf("enqueue retry: %w", err))
			return nil, err
		}
	}

	s.finalizeSet(&set.ID)
	return s.sets.FindByID(set.ID)
}

// ErrNothingToRetry is returned when a recovery point has no failed artifacts.
var ErrNothingToRetry = errors.New("this recovery point has no failed artifacts to retry")
