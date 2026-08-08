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
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

var (
	// ErrNothingSelected is returned when a restore request selects no artifacts.
	ErrNothingSelected = errors.New("select at least one artifact to restore")
	// ErrNotRestorableHere is returned for an artifact the dashboard must not
	// restore into a live platform — see restorableFromDashboard.
	ErrNotRestorableHere = errors.New("this artifact cannot be restored from the dashboard")
)

// RestoreSelection asks for specific artifacts of a recovery point to be
// restored into this live platform.
type RestoreSelection struct {
	// ArtifactIDs are PlatformBackup rows of the set. Selecting by id rather than
	// by name means the request refers to exactly what the operator was shown,
	// not to whatever matches a name by the time the worker runs.
	ArtifactIDs []uint
	// Passphrase decrypts the artifacts. Empty uses the stored one, which is
	// right for this platform's own recovery points and wrong for a foreign one.
	Passphrase string
	// StopApps stops the applications using a volume before overwriting it, and
	// starts them afterwards. Restoring underneath a running app hands it files
	// that change while it is reading them.
	StopApps bool
}

// SelectiveRestoreReport is what the operator gets back.
type SelectiveRestoreReport struct {
	Ref       string                   `json:"ref"`
	Requested int                      `json:"requested"`
	Restored  int                      `json:"restored"`
	Results   []SelectiveRestoreResult `json:"results"`
	// StartedAt/FinishedAt bound the run; a selective restore of a large volume
	// is minutes, not seconds.
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

// SelectiveRestoreResult is one artifact's outcome.
type SelectiveRestoreResult struct {
	ArtifactID uint   `json:"artifact_id"`
	Label      string `json:"label"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
}

// AppStopper stops and starts the applications attached to a volume, so an
// archive is not unpacked underneath a running container. Optional: without it
// a volume restore still runs, and the report says the app was left running.
type AppStopper interface {
	// StopUsers stops every application using the volume and returns their ids,
	// so the same set can be started again afterwards.
	StopUsers(ctx context.Context, volumeName string) ([]uint, error)
	StartApps(ctx context.Context, appIDs []uint) error
}

// SetAppStopper wires the quiesce-around-a-volume-restore behaviour.
func (s *Service) SetAppStopper(a AppStopper) { s.apps = a }

// RestoreSelected restores chosen artifacts of a recovery point into this platform. This is restore into
// a LIVE platform, a different operation from `miabi restore`: the control plane is already running, its
// encryption key is present, and the operator wants specific data back.
func (s *Service) RestoreSelected(ctx context.Context, set *models.PlatformBackupSet, sel RestoreSelection) (*SelectiveRestoreReport, error) {
	// Detached: a volume restore outlives the request that asked for it, and a
	// cancelled context mid-unpack leaves a half-written volume.
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
	// An explicit passphrase is for a recovery point this platform did not take.
	// It travels as an in-memory copy of the settings, so it is never persisted
	// and never becomes this platform's passphrase.
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

// restoreOne dispatches a single artifact, refusing the ones that must not be
// restored into a running platform.
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

// restoreVolumeQuiesced stops the applications using a volume, restores it, and starts them again. Unpacking an
// archive into a volume a container is reading is a corruption with extra steps. The apps are started again even
// when the restore fails — leaving a workspace down would turn a data problem into an outage.
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

// withPassphrase returns a COPY of the settings carrying an explicit passphrase, encrypted the same way a
// stored one is so every reader resolves it identically. A copy, not a mutation, and never written back:
// restoring one recovery point from another install must not silently become this platform's passphrase.
func withPassphrase(st *models.PlatformBackupSettings, passphrase string) (*models.PlatformBackupSettings, error) {
	enc, err := crypto.Encrypt(strings.TrimSpace(passphrase))
	if err != nil {
		return nil, fmt.Errorf("prepare the supplied passphrase: %w", err)
	}
	out := *st
	out.BackupPassphraseEnc = enc
	return &out, nil
}
