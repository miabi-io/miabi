// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// Retry policy for the helper containers. Most failures here are transient — an object store briefly refusing
// connections, a DNS hiccup, a node under load — and losing a nightly recovery point to one is a poor trade
// when the work is idempotent: every run writes a new timestamped artifact, so a retry cannot corrupt anything.
const (
	maxAttempts  = 3
	retryBackoff = 5 * time.Second
)

// runHelper executes a one-shot helper container, retrying transient failures. Returns the output of the
// LAST attempt, so the recorded log describes the state that was finally recorded rather than a failure
// that was recovered from.
func (s *Service) runHelper(ctx context.Context, dc docker.Client, action string, spec docker.RunSpec) (out string, err error) {
	baseName := spec.Name
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Docker refuses a duplicate container name, and a previous attempt's
		// container may still be going away.
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

// permanentFailures are messages that will say exactly the same thing on the
// third attempt as on the first. Retrying them wastes a minute and buries the
// real cause under repeated identical output.
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

// worthRetrying reports whether a failure looks transient. It defaults to YES. The classification is a
// heuristic over log text, and the asymmetry is deliberate: retrying a permanent failure costs a few
// seconds, while refusing to retry a transient one costs the recovery point.
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

// RetrySet re-runs the failed artifacts of a recovery point in place rather than taking a fresh one: the
// artifacts that DID succeed are often the expensive ones, and re-capturing them to recover from one
// failed item wastes bandwidth and storage. Successful items keep their recorded artifacts.
func (s *Service) RetrySet(ctx context.Context, set *models.PlatformBackupSet) (*models.PlatformBackupSet, error) {
	// As in CreateSet: the retried artifacts must not be cancelled by the request
	// that triggered them.
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

	// Reopen the set first: finalizeSet refuses to touch a terminal one, so a
	// retry against a still-failed set would run the items and never close it.
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

		// The identity envelope is sealed in-process; the rest go through the
		// worker exactly as they did the first time.
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
