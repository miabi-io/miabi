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
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
)

var (
	ErrSetNotInBucket = errors.New("no recovery point with that ref is in the bucket")
	// A PostgreSQL dump is not a MySQL one; finding out at restore time is too late.
	ErrEngineMismatch = errors.New("the recovery point was taken from a different engine than this instance runs")
)

// AdoptResult reports what adopting a recovery point produced.
type AdoptResult struct {
	Ref   string `json:"ref"`
	SetID uint   `json:"set_id"`
	// Adopted is the number of artifacts now restorable from the console.
	Adopted int `json:"adopted"`
	// Re-adopting is a no-op, so retrying after a partial failure is safe.
	AlreadyKnown bool `json:"already_known"`
	// Skipped artifacts stay in the bucket; recreate the database and adopt again to pick them up.
	Skipped []AdoptSkip `json:"skipped,omitempty"`
}

// AdoptSkip is one artifact that could not be attached to a local database.
type AdoptSkip struct {
	Database string `json:"database"`
	Filename string `json:"filename"`
	Reason   string `json:"reason"`
}

// AdoptSet writes rows only, matching artifacts by database name; missing databases are reported, never invented.
func (s *Service) AdoptSet(ctx context.Context, cfg *S3Config, basePath string, inst *models.DatabaseInstance, ref string) (*AdoptResult, error) {
	if s.sets == nil {
		return nil, ErrSetsUnavailable
	}
	res := &AdoptResult{Ref: ref}

	if existing, err := s.sets.FindByRef(inst.WorkspaceID, ref); err == nil && existing != nil {
		res.AlreadyKnown, res.SetID, res.Adopted = true, existing.ID, len(existing.Items)
		return res, nil
	}

	info, prefix, err := s.findSetInfo(ctx, cfg, basePath, ref)
	if err != nil {
		return nil, err
	}
	return s.adoptInfo(inst, info, prefix, cfg.Bucket, res)
}

// Split from the bucket read so the matching rules are testable without one.
func (s *Service) adoptInfo(inst *models.DatabaseInstance, info SetInfo, prefix, bucket string, res *AdoptResult) (*AdoptResult, error) {
	if info.Engine != "" && !strings.EqualFold(info.Engine, string(inst.Engine)) {
		return nil, fmt.Errorf("%w: %s vs %s", ErrEngineMismatch, info.Engine, inst.Engine)
	}

	dbs, err := s.dbs.ListDatabases(inst.ID)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]uint, len(dbs))
	for i := range dbs {
		byName[dbs[i].Name] = dbs[i].ID
	}

	set := &models.DatabaseBackupSet{
		WorkspaceID: inst.WorkspaceID,
		InstanceID:  inst.ID,
		Ref:         info.Ref,
		Trigger:     "adopted",
		Status:      models.BackupCompleted,
		Engine:      inst.Engine,
		Version:     info.Version,
		Encrypted:   info.Encrypted,
		Envelope:    info.Envelope,
		Destination: "s3",
		S3Bucket:    bucket,
		S3Path:      prefix,
		SizeBytes:   info.SizeBytes,
		CreatedAt:   info.CreatedAt,
	}
	if err := s.sets.Create(set); err != nil {
		return nil, err
	}
	res.SetID = set.ID

	for _, a := range info.Artifacts {
		dbID, ok := byName[a.Database]
		if !ok {
			res.Skipped = append(res.Skipped, AdoptSkip{
				Database: a.Database, Filename: a.Filename,
				Reason: "no database of that name on this instance",
			})
			continue
		}
		item := &models.Backup{
			WorkspaceID: inst.WorkspaceID,
			DatabaseID:  dbID,
			SetID:       &set.ID,
			Engine:      inst.Engine,
			ServerID:    inst.ServerID,
			Status:      models.BackupCompleted,
			Trigger:     "adopted",
			Destination: "s3",
			S3Bucket:    bucket,
			S3Path:      prefix,
			Filename:    a.Filename,
			SizeBytes:   a.SizeBytes,
			Encrypted:   a.Encrypted,
			Version:     info.Version,
			Comment:     "adopted from " + info.Ref,
			CreatedAt:   info.CreatedAt,
		}
		if err := s.repo.Create(item); err != nil {
			res.Skipped = append(res.Skipped, AdoptSkip{
				Database: a.Database, Filename: a.Filename, Reason: err.Error(),
			})
			continue
		}
		res.Adopted++
	}

	logger.Info("adopted a recovery point from the bucket",
		"set", set.Ref, "instance", inst.Name, "adopted", res.Adopted, "skipped", len(res.Skipped))
	return res, nil
}

// findSetInfo locates one descriptor by ref and returns it with its prefix.
func (s *Service) findSetInfo(ctx context.Context, cfg *S3Config, basePath, ref string) (SetInfo, string, error) {
	store, err := blobStoreFor(cfg)
	if err != nil {
		return SetInfo{}, "", err
	}
	base := strings.Trim(strings.TrimSpace(basePath), "/")
	objects, err := store.List(ctx, base)
	if err != nil {
		return SetInfo{}, "", fmt.Errorf("list %s: %w", cfg.Bucket, err)
	}
	for _, o := range objects {
		if path.Base(o.Key) != SetInfoObject {
			continue
		}
		body, gerr := store.GetBytes(ctx, o.Key)
		if gerr != nil {
			continue
		}
		var info SetInfo
		if json.Unmarshal(body, &info) != nil || info.Ref != ref {
			continue
		}
		if info.Schema != SetInfoSchema {
			return SetInfo{}, "", fmt.Errorf("descriptor schema %d is not supported by this build (expected %d)", info.Schema, SetInfoSchema)
		}
		return info, path.Dir(o.Key), nil
	}
	return SetInfo{}, "", ErrSetNotInBucket
}
