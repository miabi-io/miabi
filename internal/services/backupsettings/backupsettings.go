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

// Package backupsettings manages a workspace's shared S3 backup target; the secret is encrypted at rest.
package backupsettings

import (
	"context"
	"errors"
	"fmt"
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
)

type EnvelopeRotator interface {
	RewrapSets(workspaceID uint, oldPassphrase, newPassphrase string) (int, error)
	SealedSetCount(workspaceID uint) (int, error)
}

type Service struct {
	repo     *repositories.WorkspaceBackupSettingsRepository
	rotators []EnvelopeRotator
}

// Every service sealing points under the passphrase must be listed, or rotation strands the missed ones.
func (s *Service) SetEnvelopeRotator(rs ...EnvelopeRotator) {
	s.rotators = s.rotators[:0]
	for _, r := range rs {
		if r != nil {
			s.rotators = append(s.rotators, r)
		}
	}
}

func (s *Service) sealedCount(workspaceID uint) (int, error) {
	total := 0
	for _, r := range s.rotators {
		n, err := r.SealedSetCount(workspaceID)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// The stored passphrase changes only on success, so already-moved envelopes are undone on failure.
func (s *Service) rewrapAll(workspaceID uint, old, next string) error {
	for i, r := range s.rotators {
		if _, err := r.RewrapSets(workspaceID, old, next); err != nil {
			for j := i - 1; j >= 0; j-- {
				if _, uerr := s.rotators[j].RewrapSets(workspaceID, next, old); uerr != nil {
					return fmt.Errorf("%w (and undoing an earlier rotation failed: %v)", err, uerr)
				}
			}
			return err
		}
	}
	return nil
}

// Clearing it makes Miabi forget the only secret that opens them, which looks fine until a restore.
var ErrSealedSetsExist = errors.New("recovery points are still sealed with this passphrase; delete them first or keep the passphrase")

func NewService(repo *repositories.WorkspaceBackupSettingsRepository) *Service {
	return &Service{repo: repo}
}

// The secret is never included; S3SecretSet reports its presence.
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
	st.BackupPassphraseSet = st.BackupPassphraseEnc != ""
	st.AgeIdentitySet = st.AgeIdentityEnc != ""
	return st, nil
}

// Nil keeps a secret unchanged so the UI never round-trips it; "" removes a passphrase.
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
	BackupPassphrase   *string

	// BackupEncryption is "", "gpg" or "age"; AgeRecipients is required for "age".
	BackupEncryption string
	AgeRecipients    string
	// AgeIdentity is nil to keep the stored identity and "" to remove it.
	AgeIdentity      *string
	SkipBackupVerify bool
}

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
	if in.BundlePassphrase != nil {
		// Bundles already in the bucket still need the old passphrase; the UI warns before confirming.
		if *in.BundlePassphrase == "" {
			st.BundlePassphraseEnc = ""
		} else {
			if err := wsbundle.ValidatePassphrase(*in.BundlePassphrase); err != nil {
				return nil, err
			}
			enc, err := crypto.EncryptWS(workspaceID, *in.BundlePassphrase)
			if err != nil {
				return nil, err
			}
			st.BundlePassphraseEnc = enc
		}
	}
	if in.BackupPassphrase != nil {
		// "" clears it, the only way back to cleartext; nil means unchanged.
		var old string
		if st.BackupPassphraseEnc != "" {
			if old, err = crypto.Decrypt(st.BackupPassphraseEnc); err != nil {
				return nil, err
			}
		}
		switch *in.BackupPassphrase {
		case "":
			n, cerr := s.sealedCount(workspaceID)
			if cerr != nil {
				return nil, cerr
			}
			if n > 0 {
				return nil, ErrSealedSetsExist
			}
			st.BackupPassphraseEnc = ""
		default:
			if err := wsbundle.ValidatePassphrase(*in.BackupPassphrase); err != nil {
				return nil, err
			}
			// Rewrap before storing: on failure the stored passphrase still opens every set.
			if old != "" {
				if rerr := s.rewrapAll(workspaceID, old, *in.BackupPassphrase); rerr != nil {
					return nil, fmt.Errorf("rotate recovery point envelopes: %w", rerr)
				}
			}
			enc, err := crypto.EncryptWS(workspaceID, *in.BackupPassphrase)
			if err != nil {
				return nil, err
			}
			st.BackupPassphraseEnc = enc
		}
	}

	if err := applyAge(workspaceID, st, in); err != nil {
		return nil, err
	}
	st.SkipBackupVerify = in.SkipBackupVerify

	if err := s.repo.Upsert(st); err != nil {
		return nil, err
	}
	st.S3SecretSet = st.S3SecretKeyEnc != ""
	st.BundlePassphraseSet = st.BundlePassphraseEnc != ""
	st.BackupPassphraseSet = st.BackupPassphraseEnc != ""
	st.AgeIdentitySet = st.AgeIdentityEnc != ""
	return st, nil
}

// PrefixCheck is one prefix's result from a connection test.
type PrefixCheck struct {
	// Prefix is the path tested; empty means the bucket root.
	Prefix string `json:"prefix"`
	// Key is the object the probe wrote there.
	Key string `json:"key,omitempty"`
	// False with no Error means backups work but retention pruning won't.
	Removed bool   `json:"removed"`
	Error   string `json:"error,omitempty"`
}

// OK reports whether the prefix is usable.
func (c PrefixCheck) OK() bool { return c.Error == "" }

// An omitted secret falls back to the stored one exactly as Save does.
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

// Bucket policies are often prefix-scoped, so each prefix is tested; discovering this mid-migration is late.
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

// Client and helpers must sign for the same region.
func defaultRegion(region string) string {
	if strings.TrimSpace(region) == "" {
		return "us-east-1"
	}
	return region
}

// BundleTarget is the one place deciding a workspace can produce bundles; empty passphrase means unencrypted.
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
		return cfg, st.BundlePrefix(), "", nil
	}
	pass, err := crypto.Decrypt(st.BundlePassphraseEnc)
	if err != nil {
		return nil, "", "", err
	}
	return cfg, st.BundlePrefix(), pass, nil
}

// Path is left empty: callers append the database or volume prefix.
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

// VolumeBackupTarget satisfies volumebackup.S3Provider.
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

// DatabaseBackupPassphrase returns the workspace's backup passphrase for database backups.
func (s *Service) DatabaseBackupPassphrase(workspaceID uint) (string, error) {
	return s.BackupPassphrase(workspaceID)
}

// "" is valid, not an error: such a workspace keeps taking cleartext backups.
func (s *Service) BackupPassphrase(workspaceID uint) (string, error) {
	st, err := s.repo.FindByWorkspace(workspaceID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if st.BackupPassphraseEnc == "" {
		return "", nil
	}
	return crypto.Decrypt(st.BackupPassphraseEnc)
}

// DatabaseDestination always carries the passphrase; a hand-built Destination is how backups went out in the clear.
func (s *Service) DatabaseDestination(workspaceID uint) (backup.Destination, error) {
	pass, err := s.DatabaseBackupPassphrase(workspaceID)
	if err != nil {
		return backup.Destination{}, err
	}
	recipients, identity, err := s.BackupAge(workspaceID)
	if err != nil {
		return backup.Destination{}, err
	}
	cfg, path, err := s.DatabaseBackupTarget(workspaceID)
	if err != nil {
		return backup.Destination{}, err
	}
	// With age recipients set, new backups use age; the passphrase stays for restoring older ones.
	dest := backup.Destination{Type: "local", GPGPassphrase: pass, AgeRecipients: recipients, AgeIdentity: identity,
		SkipVerify: !s.VerifyAfterBackup(workspaceID)}
	if cfg != nil {
		cfg.Path = path
		dest.Type, dest.S3 = "s3", cfg
	}
	return dest, nil
}

// ErrInvalidAge rejects an age configuration that cannot work.
var ErrInvalidAge = errors.New("invalid age encryption settings")

// Recipients are checked here so a typo is a save error, not silently cleartext backups.
func applyAge(workspaceID uint, st *models.WorkspaceBackupSettings, in SaveInput) error {
	mode := strings.ToLower(strings.TrimSpace(in.BackupEncryption))
	if mode != "" && mode != "gpg" && mode != "age" {
		return fmt.Errorf("%w: encryption must be gpg or age", ErrInvalidAge)
	}
	recipients, err := normalizeAgeRecipients(in.AgeRecipients)
	if err != nil {
		return err
	}
	if mode == "age" && recipients == "" {
		return fmt.Errorf("%w: age encryption needs at least one recipient (age1… or ssh-ed25519 …)", ErrInvalidAge)
	}
	st.BackupEncryption, st.AgeRecipients = mode, recipients
	if in.AgeIdentity != nil {
		id := strings.TrimSpace(*in.AgeIdentity)
		if id == "" {
			st.AgeIdentityEnc = ""
			return nil
		}
		if !strings.Contains(id, "AGE-SECRET-KEY-1") && !strings.Contains(id, "PRIVATE KEY") {
			return fmt.Errorf("%w: the identity must be an age secret key (AGE-SECRET-KEY-1…) or an OpenSSH private key", ErrInvalidAge)
		}
		enc, err := crypto.EncryptWS(workspaceID, id)
		if err != nil {
			return err
		}
		st.AgeIdentityEnc = enc
	}
	return nil
}

func normalizeAgeRecipients(text string) (string, error) {
	var out []string
	for _, line := range strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == ',' }) {
		r := strings.TrimSpace(line)
		if r == "" || strings.HasPrefix(r, "#") {
			continue
		}
		if !strings.HasPrefix(r, "age1") && !strings.HasPrefix(r, "ssh-ed25519 ") && !strings.HasPrefix(r, "ssh-rsa ") {
			return "", fmt.Errorf("%w: %q is not an age recipient (age1… or ssh-ed25519 …)", ErrInvalidAge, r)
		}
		out = append(out, r)
	}
	return strings.Join(out, "\n"), nil
}

// The identity is returned in any mode so age backups still restore after switching back to GPG.
func (s *Service) BackupAge(workspaceID uint) (recipients, identity string, err error) {
	st, err := s.repo.FindByWorkspace(workspaceID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	if st.AgeIdentityEnc != "" {
		if identity, err = crypto.Decrypt(st.AgeIdentityEnc); err != nil {
			return "", "", err
		}
	}
	if st.BackupEncryption == "age" {
		recipients = st.AgeRecipients
	}
	return recipients, identity, nil
}

// VerifyAfterBackup reports whether each backup is read back right after it is written.
func (s *Service) VerifyAfterBackup(workspaceID uint) bool {
	st, err := s.repo.FindByWorkspace(workspaceID)
	return err != nil || !st.SkipBackupVerify
}
