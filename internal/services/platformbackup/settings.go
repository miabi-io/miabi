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
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/dr"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"gorm.io/gorm"
)

// SetEnv values override the stored row and are reported as env-locked.
func (s *Service) SetEnv(cfg config.PlatformBackupConfig) { s.env = cfg }

// GetSettings never includes secrets; the *Set booleans report their presence.
func (s *Service) GetSettings() (*models.PlatformBackupSettings, error) {
	st, err := s.getSettings()
	if err != nil {
		return nil, err
	}
	st.S3SecretSet = st.S3SecretKeyEnc != ""
	st.PassphraseSet = st.BackupPassphraseEnc != ""
	st.EnvLocked = s.envLockedFields()
	return st, nil
}

// Every read goes through here, making the environment authoritative rather than a seed.
func (s *Service) getSettings() (*models.PlatformBackupSettings, error) {
	st, err := s.settings.Get()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		st = &models.PlatformBackupSettings{S3Enabled: true}
	} else if err != nil {
		return nil, err
	}
	s.applyEnv(st)
	st.Normalize()
	return st, nil
}

func (s *Service) applyEnv(st *models.PlatformBackupSettings) {
	e := s.env
	if e.Configured() {
		st.S3Enabled = true
		st.S3Endpoint = e.S3Endpoint
		st.S3Bucket = e.S3Bucket
		st.S3Region = e.S3Region
		st.S3AccessKey = e.S3AccessKey
		st.S3UseSSL = e.S3UseSSL
		st.S3ForcePathStyle = e.S3ForcePathStyle
		// Memory only: storing an env secret would copy it into every backup of that database.
		st.S3SecretKeyEnc = envSecretMarker
	}
	if e.RootPath != "" {
		st.RootPath = e.RootPath
	}
	if e.DatabasePath != "" {
		st.DatabaseBackupPath = e.DatabasePath
	}
	if e.VolumePath != "" {
		st.VolumeBackupPath = e.VolumePath
	}
	if e.Passphrase != "" {
		st.BackupPassphraseEnc = envSecretMarker
		// A supplied passphrase means "encrypt with this"; requiring a second variable would leave artifacts in the clear.
		if !e.EncryptSet {
			st.EncryptBackups = true
		}
	}
	if e.EncryptSet {
		st.EncryptBackups = e.Encrypt
	}
	if e.ScheduleCron != "" {
		st.ScheduleEnabled, st.ScheduleCron = true, e.ScheduleCron
	}
	if e.MaxBackups > 0 {
		st.MaxBackups = e.MaxBackups
	}
	if e.RetentionDays > 0 {
		st.RetentionDays = e.RetentionDays
	}
	// An env passphrase lets unattended installs seal identity envelopes, so it implies the envelope unless overridden.
	if e.Passphrase != "" && !e.IncludeIdentitySet {
		st.IncludeIdentity = true
	}
	if e.IncludeIdentitySet {
		st.IncludeIdentity = e.IncludeIdentity
	}
	if e.IncludeTenantDataSet {
		st.IncludeTenantData = e.IncludeTenantData
	}
}

// Stands in for an env-held secret so presence checks work without storing or returning the value.
const envSecretMarker = "env:"

// Lets the UI disable these instead of accepting an edit that would be ignored.
func (s *Service) envLockedFields() []string {
	e := s.env
	var out []string
	if e.Configured() {
		out = append(out, "s3_endpoint", "s3_bucket", "s3_region", "s3_access_key",
			"s3_secret_key", "s3_use_ssl", "s3_force_path_style", "s3_enabled")
	}
	for field, set := range map[string]bool{
		"root_path":            e.RootPath != "",
		"database_backup_path": e.DatabasePath != "",
		"volume_backup_path":   e.VolumePath != "",
		"backup_passphrase":    e.Passphrase != "",
		// A passphrase implies encryption (see applyEnv), so the toggle is env-controlled too.
		"encrypt_backups":     e.EncryptSet || e.Passphrase != "",
		"include_identity":    e.IncludeIdentitySet,
		"include_tenant_data": e.IncludeTenantDataSet,
		"schedule_cron":       e.ScheduleCron != "",
		"max_backups":         e.MaxBackups > 0,
		"retention_days":      e.RetentionDays > 0,
	} {
		if set {
			out = append(out, field)
		}
	}
	sort.Strings(out)
	return out
}

// A nil/empty S3SecretKey keeps the stored secret, so the UI never round-trips it.
type SaveInput struct {
	S3Enabled        bool
	S3Endpoint       string
	S3Bucket         string
	S3Region         string
	S3AccessKey      string
	S3SecretKey      *string
	S3UseSSL         bool
	S3ForcePathStyle bool

	RootPath           string
	DatabaseBackupPath string
	VolumeBackupPath   string

	EncryptBackups bool
	// BackupPassphrase is nil/empty to keep the stored passphrase unchanged.
	BackupPassphrase  *string
	IncludeIdentity   bool
	IncludeTenantData bool

	ScheduleEnabled bool
	ScheduleCron    string
	MaxBackups      int
	RetentionDays   int
	Volumes         []string
}

// SaveSettings writes against the raw row so the first save cannot bake this process's environment into the DB.
func (s *Service) SaveSettings(in SaveInput) (*models.PlatformBackupSettings, error) {
	st, err := s.rawSettings()
	if err != nil {
		return nil, err
	}

	st.S3Enabled = in.S3Enabled
	st.S3Endpoint = in.S3Endpoint
	st.S3Bucket = in.S3Bucket
	st.S3Region = in.S3Region
	st.S3AccessKey = in.S3AccessKey
	st.S3UseSSL = in.S3UseSSL
	st.S3ForcePathStyle = in.S3ForcePathStyle
	st.RootPath = in.RootPath
	st.DatabaseBackupPath = in.DatabaseBackupPath
	st.VolumeBackupPath = in.VolumeBackupPath
	st.EncryptBackups = in.EncryptBackups
	st.IncludeIdentity = in.IncludeIdentity
	st.IncludeTenantData = in.IncludeTenantData
	st.ScheduleEnabled = in.ScheduleEnabled
	st.ScheduleCron = in.ScheduleCron
	st.MaxBackups = in.MaxBackups
	st.RetentionDays = in.RetentionDays
	st.Volumes = in.Volumes

	// Only replace the secret when a non-empty new value is supplied.
	if in.S3SecretKey != nil && *in.S3SecretKey != "" {
		enc, err := crypto.Encrypt(*in.S3SecretKey)
		if err != nil {
			return nil, err
		}
		st.S3SecretKeyEnc = enc
	}
	if in.BackupPassphrase != nil && *in.BackupPassphrase != "" {
		if err := dr.ValidatePassphrase(*in.BackupPassphrase); err != nil {
			return nil, err
		}
		enc, err := crypto.Encrypt(*in.BackupPassphrase)
		if err != nil {
			return nil, err
		}
		st.BackupPassphraseEnc = enc
	}

	if err := s.settings.Upsert(st); err != nil {
		return nil, err
	}

	// Validate the effective config (stored plus env), so an unrestorable setup is refused now, not at restore.
	effective, err := s.getSettings()
	if err != nil {
		return nil, err
	}
	if effective.S3Enabled && effective.S3Bucket == "" {
		return nil, ErrSetNeedsS3
	}
	if effective.ScheduleEnabled && effective.ScheduleCron == "" {
		return nil, errors.New("a cron expression is required when the schedule is enabled")
	}
	// A passphrase is optional; only encryption or an envelope with nothing to encrypt them is refused.
	if effective.BackupPassphraseEnc == "" {
		switch {
		case effective.EncryptBackups:
			return nil, ErrNoPassphrase
		case effective.IncludeIdentity:
			return nil, fmt.Errorf("sealing the identity envelope requires a backup passphrase: %w", ErrNoPassphrase)
		}
	}
	effective.S3SecretSet = effective.S3SecretKeyEnc != ""
	effective.PassphraseSet = effective.BackupPassphraseEnc != ""
	effective.EnvLocked = s.envLockedFields()
	return effective, nil
}

// No overlay or normalization: the form a save must write back.
func (s *Service) rawSettings() (*models.PlatformBackupSettings, error) {
	st, err := s.settings.Get()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &models.PlatformBackupSettings{}, nil
	}
	if err != nil {
		return nil, err
	}
	return st, nil
}

// Never logged; leaves the process only as GPG_PASSPHRASE in a one-shot container's environment.
func (s *Service) passphrase(st *models.PlatformBackupSettings) (string, error) {
	switch st.BackupPassphraseEnc {
	case "":
		return "", nil
	case envSecretMarker:
		return s.env.Passphrase, nil
	default:
		return crypto.Decrypt(st.BackupPassphraseEnc)
	}
}

// The tools encrypt to "<artifact>.gpg" when GPG_PASSPHRASE is set and decrypt transparently on restore.
func (s *Service) gpgEnv(st *models.PlatformBackupSettings) ([]string, bool, error) {
	if !st.EncryptBackups {
		return nil, false, nil
	}
	pass, err := s.passphrase(st)
	if err != nil {
		return nil, false, err
	}
	if pass == "" {
		// Degrade rather than fail, matching CreateSet; the run is recorded as unencrypted.
		logger.Warn("backup encryption is enabled but no passphrase is set: writing this artifact UNENCRYPTED")
		return nil, false, nil
	}
	return []string{"GPG_PASSPHRASE=" + pass}, true, nil
}

// Keyed off the artifact, so turning encryption off never makes old encrypted backups unrestorable.
func (s *Service) restoreGPGEnv(b *models.PlatformBackup, st *models.PlatformBackupSettings) ([]string, error) {
	if !b.Encrypted && !backup.IsEncryptedArtifact(b.Filename) {
		return nil, nil
	}
	pass, err := s.passphrase(st)
	if err != nil {
		return nil, err
	}
	if pass == "" {
		return nil, fmt.Errorf("this backup is encrypted but no backup passphrase is set: %w", ErrNoPassphrase)
	}
	return []string{"GPG_PASSPHRASE=" + pass}, nil
}

// Path is left empty; callers append the database/volume prefix.
func (s *Service) s3Config(st *models.PlatformBackupSettings) (*backup.S3Config, error) {
	if !st.S3Enabled || st.S3Bucket == "" {
		return nil, nil
	}
	secret := s.env.S3SecretKey
	if st.S3SecretKeyEnc != envSecretMarker {
		var err error
		if secret, err = crypto.Decrypt(st.S3SecretKeyEnc); err != nil {
			return nil, err
		}
	}
	return &backup.S3Config{
		Endpoint:       st.S3Endpoint,
		Bucket:         st.S3Bucket,
		Region:         s3Region(st.S3Region),
		AccessKey:      st.S3AccessKey,
		SecretKey:      secret,
		UseSSL:         effectiveUseSSL(st.S3Endpoint, st.S3UseSSL),
		ForcePathStyle: st.S3ForcePathStyle,
	}, nil
}

// MinIO ignores the region, but the AWS SDK in the *-bkup helpers refuses to start without one.
const defaultS3Region = "us-east-1"

func s3Region(configured string) string {
	if r := strings.TrimSpace(configured); r != "" {
		return r
	}
	return defaultS3Region
}

// An endpoint's scheme overrides USE_SSL so the Go client and helpers never pick different transports.
func effectiveUseSSL(endpoint string, configured bool) bool {
	switch {
	case strings.HasPrefix(endpoint, "http://"):
		return false
	case strings.HasPrefix(endpoint, "https://"):
		return true
	default:
		return configured
	}
}

// Gates volume backups in the UI, which have no local destination.
func (s *Service) S3Configured() bool {
	st, err := s.getSettings()
	if err != nil {
		return false
	}
	cfg, err := s.s3Config(st)
	return err == nil && cfg != nil
}
