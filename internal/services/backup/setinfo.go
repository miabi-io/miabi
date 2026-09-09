// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/blob"
)

// SetInfoSchema is the descriptor's payload version. A reader refuses a schema it
// does not know rather than guessing at fields.
const SetInfoSchema = 1

// SetInfoObject is the descriptor's name within a set's prefix.
const SetInfoObject = "info.json"

// SetInfo is the CLEARTEXT descriptor written beside a recovery point's artifacts.
//
// Cleartext is the point, copied from platformbackup's discovery reader: you must
// be able to list what a bucket holds without the passphrase, or a user who has
// lost their secret cannot even see what they have lost. It carries the shape of
// the data — never any of it.
//
// The sealed envelope rides along because it is the one piece a restore cannot
// reconstruct and cannot read without the passphrase anyway. Without it here, a
// set would be unreadable the moment the platform that took it was gone, which is
// precisely the case this exists for.
type SetInfo struct {
	Schema int `json:"schema"`

	Ref       string    `json:"ref"`
	Instance  string    `json:"instance"`
	Engine    string    `json:"engine"`
	Version   string    `json:"version,omitempty"`
	Trigger   string    `json:"trigger,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	SizeBytes int64     `json:"size_bytes"`

	Encrypted bool `json:"encrypted"`
	// Envelope seals this set's data key under the workspace backup passphrase.
	// Ciphertext: useless to a reader who does not hold the passphrase.
	Envelope string `json:"envelope,omitempty"`

	Artifacts []SetInfoArtifact `json:"artifacts"`
}

// SetInfoArtifact is one database's dump within the set.
type SetInfoArtifact struct {
	Database  string `json:"database"`
	Filename  string `json:"filename"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	Encrypted bool   `json:"encrypted"`
}

// SetPrefix is where a recovery point's objects live: one directory per set, under
// one per instance, beneath the workspace's database backup path. Grouping matters
// because discovery reads a set as a unit, and a flat prefix shared by every set
// would make that a filename-parsing exercise.
func SetPrefix(base, instance, ref string) string {
	return path.Join(strings.Trim(strings.TrimSpace(base), "/"), instance, ref)
}

// writeSetInfo publishes the descriptor beside the artifacts it describes.
func writeSetInfo(ctx context.Context, cfg *S3Config, prefix string, info SetInfo) error {
	store, err := blobStoreFor(cfg)
	if err != nil {
		return err
	}
	info.Schema = SetInfoSchema
	body, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("encode set info: %w", err)
	}
	return store.Put(ctx, path.Join(prefix, SetInfoObject), body)
}

// setInfoFor builds the descriptor from a finished set and its items.
func setInfoFor(set *models.DatabaseBackupSet, instance string, dbNames map[uint]string) SetInfo {
	info := SetInfo{
		Ref:       set.Ref,
		Instance:  instance,
		Engine:    string(set.Engine),
		Version:   set.Version,
		Trigger:   set.Trigger,
		CreatedAt: set.CreatedAt,
		SizeBytes: set.SizeBytes,
		Encrypted: set.Encrypted,
		Envelope:  set.Envelope,
	}
	if info.CreatedAt.IsZero() && set.StartedAt != nil {
		info.CreatedAt = *set.StartedAt
	}
	for i := range set.Items {
		it := &set.Items[i]
		if it.Filename == "" {
			continue
		}
		info.Artifacts = append(info.Artifacts, SetInfoArtifact{
			Database:  dbNames[it.DatabaseID],
			Filename:  it.Filename,
			SizeBytes: it.SizeBytes,
			Encrypted: it.Encrypted,
		})
	}
	return info
}

// blobStoreFor opens the object store for an S3 destination.
func blobStoreFor(cfg *S3Config) (*blob.Store, error) {
	if cfg == nil {
		return nil, ErrS3Required
	}
	return blob.New(blob.Config{
		Endpoint:       cfg.Endpoint,
		Bucket:         cfg.Bucket,
		Region:         cfg.Region,
		AccessKey:      cfg.AccessKey,
		SecretKey:      cfg.SecretKey,
		UseSSL:         cfg.UseSSL,
		ForcePathStyle: cfg.ForcePathStyle,
	})
}
