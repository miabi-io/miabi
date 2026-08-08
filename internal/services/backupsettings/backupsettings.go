// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package backupsettings manages a workspace's shared S3 backup target: the
// single bucket + credentials and the database/volume path prefixes that both
// database and volume backups draw from. The S3 secret is encrypted at rest.
package backupsettings

import (
	"context"
	"errors"
	"strings"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/storage/blob"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"github.com/miabi-io/miabi/internal/wsbundle"
	"gorm.io/gorm"
)

var (
	// ErrS3NotConfigured means the workspace has no usable S3 target.
	ErrS3NotConfigured = errors.New("workspace S3 backup settings are not configured")
	// ErrNoPassphrase means no bundle passphrase is stored. A bundle without one
	// would either refuse to seal or, worse, write a workspace's whole vault to a
	// bucket in the clear — so it is a hard stop, not a degraded mode.
	ErrNoPassphrase = errors.New("no bundle passphrase is set for this workspace")
)

type Service struct {
	repo *repositories.WorkspaceBackupSettingsRepository
}

func NewService(repo *repositories.WorkspaceBackupSettingsRepository) *Service {
	return &Service{repo: repo}
}

// Get returns the workspace's settings, or an empty (unsaved) record when none
// exist yet. The secret is never included; S3SecretSet reports its presence.
func (s *Service) Get(workspaceID uint) (*models.WorkspaceBackupSettings, error) {
	st, err := s.repo.FindByWorkspace(workspaceID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &models.WorkspaceBackupSettings{WorkspaceID: workspaceID}, nil
	}
	if err != nil {
		return nil, err
	}
	st.S3SecretSet = st.S3SecretKeyEnc != ""
	st.BundlePassphraseSet = st.BundlePassphraseEnc != ""
	return st, nil
}

// SaveInput carries the desired settings. S3SecretKey and BundlePassphrase are
// nil/empty to keep the stored value unchanged (so the UI never needs to
// round-trip a secret it is not allowed to read back).
type SaveInput struct {
	S3Enabled        bool
	S3Endpoint       string
	S3Bucket         string
	S3Region         string
	S3AccessKey      string
	S3SecretKey      *string
	S3UseSSL         bool
	S3ForcePathStyle bool

	DatabaseBackupPath string
	VolumeBackupPath   string
	BundlePath         string
	BundlePassphrase   *string
}

// Save upserts the workspace's settings, encrypting the secret when a new one is
// supplied and preserving the existing secret otherwise.
func (s *Service) Save(workspaceID uint, in SaveInput) (*models.WorkspaceBackupSettings, error) {
	st, err := s.repo.FindByWorkspace(workspaceID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if st == nil || errors.Is(err, gorm.ErrRecordNotFound) {
		st = &models.WorkspaceBackupSettings{WorkspaceID: workspaceID}
	}

	st.S3Enabled = in.S3Enabled
	st.S3Endpoint = in.S3Endpoint
	st.S3Bucket = in.S3Bucket
	st.S3Region = in.S3Region
	st.S3AccessKey = in.S3AccessKey
	st.S3UseSSL = in.S3UseSSL
	st.S3ForcePathStyle = in.S3ForcePathStyle
	st.DatabaseBackupPath = in.DatabaseBackupPath
	st.VolumeBackupPath = in.VolumeBackupPath
	st.BundlePath = in.BundlePath

	// Only replace the secret when a non-empty new value is supplied.
	if in.S3SecretKey != nil && *in.S3SecretKey != "" {
		enc, err := crypto.EncryptWS(workspaceID, *in.S3SecretKey)
		if err != nil {
			return nil, err
		}
		st.S3SecretKeyEnc = enc
	}
	if in.BundlePassphrase != nil && *in.BundlePassphrase != "" {
		if err := wsbundle.ValidatePassphrase(*in.BundlePassphrase); err != nil {
			return nil, err
		}
		enc, err := crypto.EncryptWS(workspaceID, *in.BundlePassphrase)
		if err != nil {
			return nil, err
		}
		st.BundlePassphraseEnc = enc
	}

	if err := s.repo.Upsert(st); err != nil {
		return nil, err
	}
	st.S3SecretSet = st.S3SecretKeyEnc != ""
	st.BundlePassphraseSet = st.BundlePassphraseEnc != ""
	return st, nil
}

// PrefixCheck is one prefix's result from a connection test.
type PrefixCheck struct {
	// Prefix is the path tested; empty means the bucket root.
	Prefix string `json:"prefix"`
	// Key is the object the probe wrote there.
	Key string `json:"key,omitempty"`
	// Removed reports whether the probe could clean up after itself. False with
	// no Error means backups will work but retention pruning will not.
	Removed bool   `json:"removed"`
	Error   string `json:"error,omitempty"`
}

// OK reports whether the prefix is usable.
func (c PrefixCheck) OK() bool { return c.Error == "" }

// TestTarget proves a target works by using it: under every prefix the workspace writes to, it puts a
// small object, reads it back and removes it. It takes the settings as the operator has them on screen,
// so an omitted secret falls back to the stored one exactly as Save does.
func (s *Service) TestTarget(ctx context.Context, workspaceID uint, in SaveInput) ([]PrefixCheck, error) {
	cfg := &backup.S3Config{
		Endpoint: in.S3Endpoint, Bucket: in.S3Bucket, Region: in.S3Region,
		AccessKey: in.S3AccessKey, UseSSL: in.S3UseSSL, ForcePathStyle: in.S3ForcePathStyle,
	}
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("an S3 bucket is required")
	}
	if strings.TrimSpace(cfg.AccessKey) == "" {
		return nil, errors.New("an access key is required")
	}
	if in.S3SecretKey != nil && *in.S3SecretKey != "" {
		cfg.SecretKey = *in.S3SecretKey
	} else {
		stored, err := s.repo.FindByWorkspace(workspaceID)
		if err != nil || stored.S3SecretKeyEnc == "" {
			return nil, errors.New("a secret key is required")
		}
		secret, dErr := crypto.Decrypt(stored.S3SecretKeyEnc)
		if dErr != nil {
			return nil, dErr
		}
		cfg.SecretKey = secret
	}

	checks := make([]PrefixCheck, 0, 3)
	for _, prefix := range distinctPrefixes(in) {
		p, err := blob.RunProbe(ctx, blob.Config{
			Endpoint: cfg.Endpoint, Bucket: cfg.Bucket, Region: defaultRegion(cfg.Region),
			AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey,
			UseSSL: cfg.UseSSL, ForcePathStyle: cfg.ForcePathStyle,
		}, prefix)
		check := PrefixCheck{Prefix: prefix, Key: p.Key, Removed: p.Removed}
		if err != nil {
			check.Error = err.Error()
		}
		checks = append(checks, check)
	}
	return checks, nil
}

// distinctPrefixes is every path the workspace writes backups to, de-duplicated. All three are tested
// rather than just one: bucket policies are commonly scoped by prefix, so a target that works for database
// dumps can still refuse the bundle tree — and finding that out during a migration is finding out late.
func distinctPrefixes(in SaveInput) []string {
	want := []string{
		strings.Trim(strings.TrimSpace(in.DatabaseBackupPath), "/"),
		strings.Trim(strings.TrimSpace(in.VolumeBackupPath), "/"),
		strings.Trim(strings.TrimSpace(in.BundlePath), "/"),
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(want))
	for _, p := range want {
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// defaultRegion mirrors what the *-bkup helpers are given, so the client and
// the helpers sign for the same region.
func defaultRegion(region string) string {
	if strings.TrimSpace(region) == "" {
		return "us-east-1"
	}
	return region
}

// BundleTarget returns the workspace's S3 config, the bundle prefix and the bundle passphrase, or
// ErrBundleNotConfigured when the pieces a bundle needs are missing. It is the one place that decides a
// workspace is ready to produce or read a portable bundle.
func (s *Service) BundleTarget(workspaceID uint) (*backup.S3Config, string, string, error) {
	cfg, err := s.S3ConfigFor(workspaceID)
	if err != nil {
		return nil, "", "", err
	}
	if cfg == nil {
		return nil, "", "", ErrS3NotConfigured
	}
	st, err := s.repo.FindByWorkspace(workspaceID)
	if err != nil {
		return nil, "", "", err
	}
	if st.BundlePassphraseEnc == "" {
		return nil, "", "", ErrNoPassphrase
	}
	pass, err := crypto.Decrypt(st.BundlePassphraseEnc)
	if err != nil {
		return nil, "", "", err
	}
	return cfg, st.BundlePrefix(), pass, nil
}

// S3ConfigFor returns the decrypted S3 config for a workspace, or nil when S3 is
// not enabled/configured. The Path field is left empty: callers append the
// database or volume prefix. Consumed by database + volume backups.
func (s *Service) S3ConfigFor(workspaceID uint) (*backup.S3Config, error) {
	st, err := s.repo.FindByWorkspace(workspaceID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !st.S3Enabled || st.S3Bucket == "" {
		return nil, nil
	}
	secret, err := crypto.Decrypt(st.S3SecretKeyEnc)
	if err != nil {
		return nil, err
	}
	return &backup.S3Config{
		Endpoint:       st.S3Endpoint,
		Bucket:         st.S3Bucket,
		Region:         st.S3Region,
		AccessKey:      st.S3AccessKey,
		SecretKey:      secret,
		UseSSL:         st.S3UseSSL,
		ForcePathStyle: st.S3ForcePathStyle,
	}, nil
}

// VolumeBackupTarget returns the workspace's S3 config plus the volume backup
// path prefix, or (nil, "", nil) when S3 is not enabled/configured. Satisfies
// the volumebackup.S3Provider interface.
func (s *Service) VolumeBackupTarget(workspaceID uint) (*backup.S3Config, string, error) {
	cfg, err := s.S3ConfigFor(workspaceID)
	if err != nil || cfg == nil {
		return nil, "", err
	}
	st, err := s.repo.FindByWorkspace(workspaceID)
	if err != nil {
		return nil, "", err
	}
	return cfg, st.VolumeBackupPath, nil
}

// DatabaseBackupTarget returns the workspace's S3 config plus the database
// backup path prefix, or (nil, "", nil) when S3 is not enabled/configured.
// Database backups use this as their destination when configured.
func (s *Service) DatabaseBackupTarget(workspaceID uint) (*backup.S3Config, string, error) {
	cfg, err := s.S3ConfigFor(workspaceID)
	if err != nil || cfg == nil {
		return nil, "", err
	}
	st, err := s.repo.FindByWorkspace(workspaceID)
	if err != nil {
		return nil, "", err
	}
	return cfg, st.DatabaseBackupPath, nil
}
