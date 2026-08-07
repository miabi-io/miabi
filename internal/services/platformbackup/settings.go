// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// SetEnv wires the environment-supplied platform backup configuration. Values
// present there override the stored row and are reported as env-locked.
func (s *Service) SetEnv(cfg config.PlatformBackupConfig) { s.env = cfg }

// GetSettings returns the platform backup settings, or an empty (unsaved) record
// when none exist yet. Secrets are never included; the *Set booleans report
// their presence so the UI can render "••••• (set)".
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

// getSettings loads the single settings row, applies the environment overlay and
// normalizes the derived paths.
//
// Every read goes through here, which is what makes the environment authoritative
// rather than merely a seed: an operator cannot end up with a stored value that
// disagrees with the process configuration and wonder which one is running.
func (s *Service) getSettings() (*models.PlatformBackupSettings, error) {
	st, err := s.settings.Get()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// A never-configured platform starts with S3 selected. It is the only
		// destination there is, so presenting it as an unchecked option would be
		// offering a choice that does not exist — and would hide the very fields
		// the operator came here to fill in. An admin who saves it off has made a
		// real decision, and that row is no longer new.
		st = &models.PlatformBackupSettings{S3Enabled: true}
	} else if err != nil {
		return nil, err
	}
	s.applyEnv(st)
	st.Normalize()
	return st, nil
}

// applyEnv overlays the environment configuration onto a stored settings row.
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
		// Held in memory only. Writing an env-supplied secret into the database
		// would copy it into every backup of that database, for no benefit.
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
		// Supplying a passphrase is the operator saying "encrypt with this".
		// Requiring a second variable to act on it would leave artifacts in the
		// clear on a deployment that plainly asked otherwise.
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
	// An env-supplied passphrase is what makes an unattended install able to seal
	// identity envelopes without anyone opening the UI — so it implies the
	// envelope unless the operator explicitly said otherwise.
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

// envSecretMarker stands in for a secret that lives in the process environment
// rather than the database, so presence checks and the *Set booleans work
// without the value ever being stored or returned.
const envSecretMarker = "env:"

// envLockedFields names the settings this deployment takes from the environment,
// so the UI can disable them instead of accepting an edit that would be ignored.
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
		// A passphrase implies encryption (see applyEnv), so the toggle is just as
		// env-controlled as if _ENCRYPT had been set explicitly.
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

// SaveInput carries the desired platform backup settings. S3SecretKey is
// nil/empty to keep the stored secret unchanged (so the UI never round-trips it).
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

// SaveSettings upserts the platform backup settings, encrypting the S3 secret
// (platform scope) when a new one is supplied and preserving it otherwise.
//
// It writes against the RAW stored row, not the environment-overlaid one that
// reads return. Otherwise the first save would bake this process's environment
// into the database, and the settings would stop tracking the environment the
// moment someone edited an unrelated field.
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

	// Validate what will actually run — the stored row plus the environment —
	// rather than what was typed. A configuration that cannot produce a restorable
	// artifact is refused here, not discovered at the next backup or, worse, at
	// the restore.
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
	// A passphrase is OPTIONAL. Backing up an unencrypted platform is a legitimate
	// choice — a private bucket the operator already trusts, a lab, a first run
	// before key custody is arranged — and refusing it means no backup at all,
	// which is strictly worse than an unencrypted one.
	//
	// What is refused is asking for something impossible: encryption or an
	// identity envelope with nothing to encrypt them with. Those are explicit
	// requests, and silently ignoring them would leave an operator believing in
	// protection they do not have.
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

// rawSettings loads the stored row with no environment overlay and no
// normalization — the form a save must write back.
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

// passphrase returns the decrypted backup passphrase, or "" when none is set.
// It is never logged and never leaves the process except as GPG_PASSPHRASE in a
// one-shot container's environment.
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

// gpgEnv returns the encryption environment for a *-bkup one-shot: the tools
// encrypt to "<artifact>.gpg" when GPG_PASSPHRASE is present, and transparently
// decrypt a ".gpg" artifact on restore with the same variable. Returns nil (and
// encrypted=false) when encryption is off, so an unencrypted run is unchanged.
func (s *Service) gpgEnv(st *models.PlatformBackupSettings) ([]string, bool, error) {
	if !st.EncryptBackups {
		return nil, false, nil
	}
	pass, err := s.passphrase(st)
	if err != nil {
		return nil, false, err
	}
	if pass == "" {
		// Degrade rather than fail, matching CreateSet: a passphrase that has gone
		// missing must not turn every artifact into a failure. The run is recorded
		// as unencrypted — which is what it is — and the warning names the cause.
		logger.Warn("backup encryption is enabled but no passphrase is set: writing this artifact UNENCRYPTED")
		return nil, false, nil
	}
	return []string{"GPG_PASSPHRASE=" + pass}, true, nil
}

// restoreGPGEnv supplies the passphrase for restoring an artifact that was
// written encrypted. It keys off the artifact, not the current setting: turning
// encryption off must not make yesterday's encrypted backups unrestorable.
func (s *Service) restoreGPGEnv(b *models.PlatformBackup, st *models.PlatformBackupSettings) ([]string, error) {
	if !b.Encrypted && !strings.HasSuffix(b.Filename, ".gpg") {
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

// s3Config returns the decrypted S3 config for the platform target, or nil when
// S3 is not enabled/configured. The Path is left empty; callers append the
// database/volume prefix.
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

// defaultS3Region is sent when the operator configures none.
//
// MinIO and most S3-compatible stores ignore the region entirely, so leaving it
// blank looks harmless — but the AWS SDK inside the *-bkup helpers refuses to
// start without one ("missing environment variables: [AWS_REGION]"), and the
// failure lands on the artifact rather than on the setting. The Go client
// already defaulted it; sending a different value to the helpers than the client
// uses would be worse than sending none, so both use this.
const defaultS3Region = "us-east-1"

// s3Region returns the configured region, or the default when unset.
func s3Region(configured string) string {
	if r := strings.TrimSpace(configured); r != "" {
		return r
	}
	return defaultS3Region
}

// effectiveUseSSL resolves the transport when the endpoint states one.
//
// "http://minio:9000" with USE_SSL=true is a contradiction, and it is an easy
// one to write — the scheme is typed once and the flag left at its default. The
// scheme is the more specific statement, so it wins. Leaving the two to disagree
// means the Go client and the *-bkup helpers can pick different transports, and
// the failure surfaces as an upload that reports success and lands nowhere.
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

// S3Configured reports whether a usable S3 target is set (the UI uses it to gate
// volume backups, which have no local destination).
func (s *Service) S3Configured() bool {
	st, err := s.getSettings()
	if err != nil {
		return false
	}
	cfg, err := s.s3Config(st)
	return err == nil && cfg != nil
}
