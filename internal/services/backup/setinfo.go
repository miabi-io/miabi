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

// A reader refuses an unknown schema rather than guessing at fields.
const SetInfoSchema = 1

// SetInfoObject is the descriptor's name within a set's prefix.
const SetInfoObject = "info.json"

// SetInfo is cleartext so a bucket can be listed without the passphrase; it carries shape, never data.
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
	// Ciphertext: useless without the passphrase.
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

// Discovery reads a set as a unit, so each set gets its own directory.
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
