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

// Package restores runs database and volume restores after their request, since a large one outlasts any HTTP timeout.
package restores

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

const (
	maxDuration    = 24 * time.Hour
	heartbeatEvery = time.Minute
	// A row silent this long belongs to a process that died, not to a slow restore.
	StaleAfter = 5 * time.Minute
)

var ErrRunning = errors.New("a restore of this target is already running")

// Report is what the requester is told when a restore ends.
type Report struct {
	Kind     models.RestoreKind
	Subject  string
	Source   string
	Link     string
	OK       bool
	Restored []string
	Failed   []string
	Err      string
}

type Reporter interface {
	RequestedRestoreFinished(workspaceID, userID uint, rep Report)
}

// Outcome lists per-database results; a single-target restore leaves it empty.
type Outcome struct {
	Restored []string
	Failed   []string
}

type Runner struct {
	repo     *repositories.RestoreRepository
	reporter Reporter
}

func NewRunner(repo *repositories.RestoreRepository) *Runner { return &Runner{repo: repo} }

func (r *Runner) SetReporter(rep Reporter) { r.reporter = rep }

// Live is the restore running against target, or nil; backups check it too, since a dump taken mid-restore is half old, half new.
func (r *Runner) Live(target models.Restore) (*models.Restore, error) {
	if r == nil {
		return nil, nil
	}
	return r.repo.Live(target, time.Now().Add(-StaleAfter))
}

// ForInstance lists an instance's recent restores, newest first.
func (r *Runner) ForInstance(workspaceID, instanceID uint) ([]models.Restore, error) {
	return r.repo.ListForInstance(workspaceID, instanceID, 20)
}

// ForVolume lists a volume's recent restores, newest first.
func (r *Runner) ForVolume(workspaceID, volumeID uint) ([]models.Restore, error) {
	return r.repo.ListForVolume(workspaceID, volumeID, 20)
}

// Start records the restore and returns once its row exists; run continues detached and the requester is told how it ended.
func (r *Runner) Start(rec *models.Restore, subject, link string, run func(ctx context.Context) (Outcome, error)) (*models.Restore, error) {
	if live, err := r.Live(*rec); err != nil {
		return nil, err
	} else if live != nil {
		return nil, ErrRunning
	}
	now := time.Now()
	rec.Status, rec.StartedAt, rec.HeartbeatAt = models.BackupRunning, &now, &now
	if err := r.repo.Create(rec); err != nil {
		return nil, err
	}
	row := *rec
	go func() {
		var (
			out Outcome
			err error
		)
		func() {
			defer func() {
				if p := recover(); p != nil {
					logger.Error("restore panicked", "restore", rec.ID, "panic", p)
					err = fmt.Errorf("restore crashed: %v", p)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), maxDuration)
			defer cancel()
			defer Beat(func() error { return r.repo.Touch(rec.ID) })()
			out, err = run(ctx)
		}()
		r.finish(rec, subject, link, out, err)
	}()
	return &row, nil
}

func (r *Runner) finish(rec *models.Restore, subject, link string, out Outcome, err error) {
	fin := time.Now()
	rec.FinishedAt, rec.Restored, rec.Failed = &fin, out.Restored, out.Failed
	switch {
	case err != nil:
		rec.Status, rec.Error = models.BackupFailed, err.Error()
	case len(out.Failed) > 0:
		rec.Status = models.BackupFailed
		rec.Error = fmt.Sprintf("%d of %d databases failed to restore", len(out.Failed), len(out.Failed)+len(out.Restored))
	default:
		rec.Status = models.BackupCompleted
	}
	if uerr := r.repo.Update(rec); uerr != nil {
		logger.Error("record restore outcome", "restore", rec.ID, "error", uerr)
	}
	if r.reporter != nil && rec.RequestedByID != nil {
		r.reporter.RequestedRestoreFinished(rec.WorkspaceID, *rec.RequestedByID, Report{
			Kind: rec.Kind, Subject: subject, Source: rec.Source, Link: link,
			OK: rec.Status == models.BackupCompleted, Restored: rec.Restored, Failed: rec.Failed, Err: rec.Error,
		})
	}
}

// FailStale closes restores a restart abandoned, so they stop blocking the target.
func (r *Runner) FailStale() error {
	n, err := r.repo.FailStale(time.Now().Add(-StaleAfter), "interrupted: Miabi restarted while this restore ran")
	if n > 0 {
		logger.Warn("closed abandoned restores", "count", n)
	}
	return err
}

// Beat touches a run's row every minute until stop is called.
func Beat(touch func() error) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(heartbeatEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_ = touch()
			}
		}
	}()
	return func() { close(done) }
}
