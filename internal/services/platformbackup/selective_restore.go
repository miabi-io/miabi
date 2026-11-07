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
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

var (
	// ErrNothingSelected is returned when a restore request selects no artifacts.
	ErrNothingSelected = errors.New("select at least one artifact to restore")
	// See restorableFromDashboard.
	ErrNotRestorableHere = errors.New("this artifact cannot be restored from the dashboard")
)

type RestoreSelection struct {
	// By id so the request means exactly what the operator was shown, not whatever matches a name later.
	ArtifactIDs []uint
	// Empty uses the stored passphrase: right for this platform's points, wrong for a foreign one.
	Passphrase string
	// Restoring under a running app hands it files that change while it reads them.
	StopApps bool
}

// SelectiveRestoreReport is what the operator gets back.
type SelectiveRestoreReport struct {
	Ref        string                   `json:"ref"`
	Requested  int                      `json:"requested"`
	Restored   int                      `json:"restored"`
	Results    []SelectiveRestoreResult `json:"results"`
	StartedAt  time.Time                `json:"started_at"`
	FinishedAt time.Time                `json:"finished_at"`
}

// SelectiveRestoreResult is one artifact's outcome.
type SelectiveRestoreResult struct {
	ArtifactID uint   `json:"artifact_id"`
	Label      string `json:"label"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
}

// Optional: without it a volume restore still runs and the report says the app was left running.
type AppStopper interface {
	// Returns the ids so the same set can be started again afterwards.
	StopUsers(ctx context.Context, volumeName string) ([]uint, error)
	StartApps(ctx context.Context, appIDs []uint) error
}

// SetAppStopper wires the quiesce-around-a-volume-restore behaviour.
func (s *Service) SetAppStopper(a AppStopper) { s.apps = a }

// RestoreSelected restores into a live platform, unlike `miabi restore`: the control plane and its key are present.
func (s *Service) RestoreSelected(ctx context.Context, set *models.PlatformBackupSet, sel RestoreSelection) (*SelectiveRestoreReport, error) {
	// Detached: a cancelled context mid-unpack leaves a half-written volume.
	ctx = context.WithoutCancel(ctx)

	if len(sel.ArtifactIDs) == 0 {
		return nil, ErrNothingSelected
	}
	st, err := s.getSettings()
	if err != nil {
		return nil, err
	}
	cfg, err := s.s3Config(st)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, ErrS3NotConfigured
	}
	// Carried as an in-memory settings copy so a foreign point's passphrase is never persisted.
	if sel.Passphrase != "" {
		if st, err = withPassphrase(st, sel.Passphrase); err != nil {
			return nil, err
		}
	}

	wanted := make(map[uint]bool, len(sel.ArtifactIDs))
	for _, id := range sel.ArtifactIDs {
		wanted[id] = true
	}

	rep := &SelectiveRestoreReport{
		Ref: set.Ref, Requested: len(sel.ArtifactIDs),
		Results: []SelectiveRestoreResult{}, StartedAt: time.Now().UTC(),
	}

	for i := range set.Items {
		item := &set.Items[i]
		if !wanted[item.ID] {
			continue
		}
		res := SelectiveRestoreResult{ArtifactID: item.ID, Label: artifactLabel(item)}
		if err := s.restoreOne(ctx, st, cfg, item, sel); err != nil {
			res.Error = err.Error()
			logger.Error("selective restore failed", "ref", set.Ref, "artifact", res.Label, "error", err)
		} else {
			res.OK = true
			rep.Restored++
			logger.Info("selective restore completed", "ref", set.Ref, "artifact", res.Label)
		}
		rep.Results = append(rep.Results, res)
	}

	rep.FinishedAt = time.Now().UTC()
	if len(rep.Results) == 0 {
		return nil, fmt.Errorf("none of the selected artifacts belong to recovery point %s", set.Ref)
	}
	return rep, nil
}

func (s *Service) restoreOne(ctx context.Context, st *models.PlatformBackupSettings, cfg *backup.S3Config, item *models.PlatformBackup, sel RestoreSelection) error {
	if item.Status != models.BackupCompleted || item.Filename == "" {
		return errors.New("this artifact never completed; there is nothing to restore")
	}
	if ok, reason := restorableFromDashboard(string(item.Subject), true); !ok {
		return fmt.Errorf("%w: %s", ErrNotRestorableHere, reason)
	}

	switch item.Subject {
	case models.PlatformBackupTenantDatabase:
		td, err := s.resolveTenantDatabase(item)
		if err != nil {
			return err
		}
		return s.restoreTenantDatabase(ctx, st, cfg, td, item)

	case models.PlatformBackupTenantVolume, models.PlatformBackupVolume:
		return s.restoreVolumeQuiesced(ctx, st, cfg, item, sel.StopApps)

	default:
		return ErrNotRestorableHere
	}
}

// Apps restart even if the restore fails: leaving a workspace down turns a data problem into an outage.
func (s *Service) restoreVolumeQuiesced(ctx context.Context, st *models.PlatformBackupSettings, cfg *backup.S3Config, item *models.PlatformBackup, stopApps bool) error {
	var stopped []uint
	if stopApps && s.apps != nil && item.VolumeName != "" {
		var err error
		if stopped, err = s.apps.StopUsers(ctx, item.VolumeName); err != nil {
			return fmt.Errorf("stop the applications using %s: %w", item.VolumeName, err)
		}
		if len(stopped) > 0 {
			logger.Info("stopped applications for a volume restore", "volume", item.VolumeName, "apps", len(stopped))
		}
		defer func() {
			if err := s.apps.StartApps(ctx, stopped); err != nil {
				logger.Error("could not start the applications again after a volume restore",
					"volume", item.VolumeName, "apps", stopped, "error", err)
			}
		}()
	}
	return s.restoreTenantVolume(ctx, st, cfg, item)
}

// A copy never written back, so a foreign recovery point's passphrase never becomes this platform's.
func withPassphrase(st *models.PlatformBackupSettings, passphrase string) (*models.PlatformBackupSettings, error) {
	enc, err := crypto.Encrypt(strings.TrimSpace(passphrase))
	if err != nil {
		return nil, fmt.Errorf("prepare the supplied passphrase: %w", err)
	}
	out := *st
	out.BackupPassphraseEnc = enc
	return &out, nil
}
