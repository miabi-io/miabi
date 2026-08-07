// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package platformbackup

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/dr"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/blob"
	"gorm.io/gorm"
)

// DiscoveredSet is a recovery point found in the bucket.
//
// The bucket is the authority, not this platform's database. After a
// control-plane restore the sets a platform knows about are whatever its dump
// happened to contain — everything taken since is still in object storage and
// invisible to it. Discovery is what makes those reachable again.
type DiscoveredSet struct {
	Ref            string    `json:"ref"`
	InstallID      string    `json:"install_id,omitempty"`
	MiabiVersion   string    `json:"miabi_version,omitempty"`
	Encrypted      bool      `json:"encrypted"`
	IdentitySealed bool      `json:"identity_sealed"`
	CreatedAt      time.Time `json:"created_at"`

	Artifacts []DiscoveredArtifact `json:"artifacts"`

	// Known is true when this platform already has the recovery point in its own
	// database; those can be verified and retried, not just restored from.
	Known bool `json:"known"`
	// SetID is the local row, when Known.
	SetID uint `json:"set_id,omitempty"`

	// Foreign marks a recovery point taken by a DIFFERENT install. Its tenant
	// data restores here perfectly well — a database dump is data. Its
	// control-plane dump would fill this platform with secrets encrypted under a
	// key it does not have, which is what KEKMatches reports.
	Foreign bool `json:"foreign"`
	// KEKMatches reports whether the recovery point was taken under this
	// platform's master key. False means anything control-plane from it is
	// unusable here, whatever the artifact list says.
	KEKMatches bool `json:"kek_matches"`
}

// DiscoveredArtifact is one restorable file in a discovered recovery point.
type DiscoveredArtifact struct {
	Subject   string `json:"subject"`
	Workspace string `json:"workspace,omitempty"`
	Database  string `json:"database,omitempty"`
	Volume    string `json:"volume,omitempty"`
	Engine    string `json:"engine,omitempty"`
	File      string `json:"file"`
	Key       string `json:"key"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	Encrypted bool   `json:"encrypted"`

	// Restorable reports whether THIS platform can restore this artifact from the
	// dashboard, and Reason says why not. The control-plane dump is never
	// restorable this way: it overwrites the database the running platform is
	// using, which is a maintenance-mode operation and not a checkbox.
	Restorable bool   `json:"restorable"`
	Reason     string `json:"reason,omitempty"`

	// Present reports whether the object is actually in the bucket. A manifest
	// records what happened when the backup ran; retention and lifecycle rules
	// act on the bucket afterwards.
	Present bool `json:"present"`
}

// DiscoverSets lists the recovery points in the configured bucket.
//
// It reads the cleartext info files — no passphrase needed to see what exists,
// which is the point of keeping them readable. Artifact presence is checked so
// the list distinguishes "recorded" from "actually there".
func (s *Service) DiscoverSets(ctx context.Context) ([]DiscoveredSet, error) {
	st, err := s.getSettings()
	if err != nil {
		return nil, err
	}
	store, err := s.blobStore(st)
	if err != nil {
		return nil, err
	}

	objects, err := store.List(ctx, st.RootPath)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", st.S3Bucket, err)
	}

	local := s.localSetsByRef()
	mine := s.installID()
	out := make([]DiscoveredSet, 0, 8)

	for _, obj := range objects {
		ref := dr.RefFromManifestObject(obj.Key)
		if ref == "" {
			continue
		}
		body, err := store.GetBytes(ctx, obj.Key)
		if err != nil {
			logger.Warn("discover: could not read a recovery point info file", "object", obj.Key, "error", err)
			continue
		}
		man, err := dr.DecodeManifest(body)
		if err != nil {
			logger.Warn("discover: unreadable recovery point info file", "object", obj.Key, "error", err)
			continue
		}

		set := DiscoveredSet{
			Ref: man.Ref, InstallID: man.InstallID, MiabiVersion: man.MiabiVersion,
			Encrypted: man.Encrypted, IdentitySealed: man.IdentitySealed, CreatedAt: man.CreatedAt,
			Foreign:    mine != "" && man.InstallID != "" && man.InstallID != mine,
			KEKMatches: s.kekMatches(man.KEKFingerprint),
		}
		if row, ok := local[man.Ref]; ok {
			set.Known, set.SetID = true, row
		}
		set.Artifacts = s.describeArtifacts(ctx, store, man)
		out = append(out, set)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// describeArtifacts turns a manifest's artifacts into what the dashboard offers.
func (s *Service) describeArtifacts(ctx context.Context, store *blob.Store, man *dr.Manifest) []DiscoveredArtifact {
	out := make([]DiscoveredArtifact, 0, len(man.Artifacts))
	for _, a := range man.Artifacts {
		key := man.ArtifactKey(a)
		d := DiscoveredArtifact{
			Subject: a.Subject, Workspace: a.Workspace, Database: a.Database,
			Volume: a.Volume, Engine: a.Engine, File: a.File, Key: key,
			SizeBytes: a.SizeBytes, Encrypted: a.Encrypted,
		}
		if found, err := store.Exists(ctx, key); err == nil {
			d.Present = found
		}
		d.Restorable, d.Reason = restorableFromDashboard(a.Subject, d.Present)
		out = append(out, d)
	}
	return out
}

// restorableFromDashboard decides what the dashboard may offer for an artifact.
//
// The control-plane dump is excluded deliberately, not for lack of plumbing:
// restoring it in place overwrites the database the running platform is using —
// including the rows describing the restore itself — and leaves the process
// running against data that no longer matches it. That is a maintenance-mode
// operation with its own confirmation, or a `miabi restore` onto a fresh host.
// It must never sit in a list of checkboxes beside a volume.
func restorableFromDashboard(subject string, present bool) (bool, string) {
	switch subject {
	case dr.SubjectIdentity:
		return false, "the identity envelope carries the encryption key; there is nothing to restore from it"
	case dr.SubjectDatabase:
		return false, "restoring the control-plane database overwrites the database this platform is running on — use the guarded control-plane restore, or `miabi restore` onto a fresh host"
	case dr.SubjectTenantDatabase, dr.SubjectTenantVolume, dr.SubjectVolume:
		if !present {
			return false, "this artifact is no longer in the bucket"
		}
		return true, ""
	default:
		return false, "unrecognised artifact type"
	}
}

// localSetsByRef maps the refs this platform already knows to their row ids.
func (s *Service) localSetsByRef() map[string]uint {
	out := map[string]uint{}
	sets, err := s.sets.List()
	if err != nil {
		return out
	}
	for i := range sets {
		out[sets[i].Ref] = sets[i].ID
	}
	return out
}

// installID reports this platform's install id, via the identity source.
func (s *Service) installID() string {
	if s.identity == nil {
		return ""
	}
	id, err := s.identity()
	if err != nil || id == nil {
		return ""
	}
	return id.InstallID
}

// kekMatches reports whether a recovery point was taken under this platform's
// master key.
func (s *Service) kekMatches(fingerprint string) bool {
	if fingerprint == "" || s.fingerprint == nil {
		return false
	}
	return s.fingerprint(models.KEKFingerprintLabel) == fingerprint
}

// ImportSet records a discovered recovery point in this platform's database, so
// it can be verified, retried and restored like any other.
//
// Needed because a restored control plane only knows the recovery points its
// dump contained — every one taken since is in the bucket and invisible to it.
// Import is idempotent on the ref.
func (s *Service) ImportSet(ctx context.Context, ref string) (*models.PlatformBackupSet, error) {
	if existing, err := s.sets.FindByRef(ref); err == nil && existing != nil {
		return existing, nil
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	st, err := s.getSettings()
	if err != nil {
		return nil, err
	}
	store, err := s.blobStore(st)
	if err != nil {
		return nil, err
	}
	body, err := store.GetBytes(ctx, dr.ManifestObject(st.RootPath, ref))
	if err != nil {
		if errors.Is(err, blob.ErrNotFound) {
			return nil, fmt.Errorf("no recovery point %q in %s", ref, st.S3Bucket)
		}
		return nil, err
	}
	man, err := dr.DecodeManifest(body)
	if err != nil {
		return nil, err
	}

	created := man.CreatedAt
	set := &models.PlatformBackupSet{
		Ref: man.Ref, Trigger: "imported", Status: models.BackupCompleted,
		InstallID: man.InstallID, MiabiVersion: man.MiabiVersion, SchemaVersion: man.SchemaVersion,
		KEKFingerprint: man.KEKFingerprint, Encrypted: man.Encrypted, IdentitySealed: man.IdentitySealed,
		Destination: destS3, S3Bucket: man.Bucket, S3Path: man.Prefix,
		StartedAt: &created, FinishedAt: &created,
	}
	if err := s.sets.Create(set); err != nil {
		return nil, err
	}

	for _, a := range man.Artifacts {
		item := &models.PlatformBackup{
			SetID: &set.ID, Subject: models.PlatformBackupSubject(a.Subject),
			VolumeName: a.Volume, WorkspaceSlug: a.Workspace, DatabaseName: a.Database, Engine: a.Engine,
			Status: models.BackupCompleted, Trigger: "imported", Destination: destS3,
			Encrypted: a.Encrypted, S3Bucket: man.Bucket, S3Path: man.ArtifactPath(a),
			Filename: a.File, SizeBytes: a.SizeBytes,
		}
		if err := s.repo.Create(item); err != nil {
			return nil, err
		}
	}

	logger.Info("imported a recovery point from the bucket", "ref", man.Ref, "artifacts", len(man.Artifacts))
	return s.sets.FindByID(set.ID)
}
