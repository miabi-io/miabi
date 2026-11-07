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

package wsbackup

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/storage/blob"
	"github.com/miabi-io/miabi/internal/wsbundle"
)

// Export records a pending export of the workspace and schedules it.
func (s *Service) Export(ctx context.Context, workspaceID uint, userID *uint, trigger string) (*models.WorkspaceBundle, error) {
	cfg, prefix, _, err := s.Settings.BundleTarget(workspaceID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	ws, err := s.Workspace.Get(workspaceID)
	if err != nil {
		return nil, err
	}
	if trigger == "" {
		trigger = "manual"
	}
	return s.start(ctx, &models.WorkspaceBundle{
		WorkspaceID:     workspaceID,
		Kind:            models.BundleExport,
		Ref:             wsbundle.NewRef(ws.Name, time.Now()),
		Status:          models.BackupPending,
		Trigger:         trigger,
		S3Bucket:        cfg.Bucket,
		S3Prefix:        prefix,
		SourceWorkspace: ws.Name,
		CreatedBy:       userID,
	})
}

// Data first, index last, so an interrupted export leaves orphans, never an index promising a restore.
func (s *Service) runExport(ctx context.Context, b *models.WorkspaceBundle) error {
	cfg, prefix, passphrase, err := s.Settings.BundleTarget(b.WorkspaceID)
	if err != nil {
		s.fail(b, fmt.Errorf("%w: %v", ErrNotConfigured, err))
		return nil
	}
	store, err := s.store(cfg)
	if err != nil {
		s.fail(b, err)
		return nil
	}

	// Probe first: otherwise a refusing bucket fails only after every dump and archive has run.
	if p, err := blob.RunProbe(ctx, blobConfig(cfg), prefix); err != nil {
		s.fail(b, fmt.Errorf("the backup target is not usable: %w", err))
		return nil
	} else if !p.Removed {
		logger.Warn("bundle: the backup target does not allow deletes; retention and bundle deletion will not work",
			"workspace", b.WorkspaceID, "bucket", cfg.Bucket)
	}

	report := &models.BundleReport{}
	info := &wsbundle.Info{
		Schema:        wsbundle.InfoSchema,
		Ref:           b.Ref,
		SourceInstall: s.InstallID,
		MiabiVersion:  s.Version,
		Encrypted:     passphrase != "",
		Bucket:        cfg.Bucket,
		Prefix:        prefix,
		CreatedAt:     time.Now().UTC(),
	}

	s.phase(b, models.BundlePhaseState)
	state, err := s.collect(b.WorkspaceID, report)
	if err != nil {
		b.Report = *report
		s.fail(b, err)
		return nil
	}
	info.Workspace = state.Workspace.Name
	info.DisplayName = state.Workspace.DisplayName
	info.Apps = len(state.Apps)
	info.Databases = len(state.Databases)
	info.Volumes = len(state.Volumes)
	info.Secrets = len(state.Secrets)
	info.Configs = len(state.Configs)
	info.Routes = len(state.Routes)
	info.Certificates = len(state.Certificates)
	info.Pipelines = len(state.Pipelines)
	info.GitOpsSources = len(state.GitSources)

	sealed, err := wsbundle.Encode(state, passphrase)
	if err != nil {
		b.Report = *report
		s.fail(b, err)
		return nil
	}

	s.phase(b, models.BundlePhaseDatabases)
	info.Artifacts = append(info.Artifacts, s.exportDatabases(ctx, b, cfg, prefix, passphrase, report)...)

	s.phase(b, models.BundlePhaseVolumes)
	info.Artifacts = append(info.Artifacts, s.exportVolumes(ctx, b, cfg, prefix, passphrase, report)...)
	sizeArtifacts(ctx, store, info.Artifacts)

	s.phase(b, models.BundlePhaseUpload)
	stateKey := wsbundle.StateObject(prefix, b.Ref, info.Encrypted)
	if err := store.Put(ctx, stateKey, sealed); err != nil {
		b.Report = *report
		s.fail(b, fmt.Errorf("upload state file: %w", err))
		return nil
	}
	info.Artifacts = append(info.Artifacts, wsbundle.Artifact{
		Subject:   wsbundle.SubjectState,
		File:      wsbundle.StateFile(b.Ref, info.Encrypted),
		Path:      wsbundle.Root(prefix, b.Ref),
		SizeBytes: int64(len(sealed)),
		Encrypted: info.Encrypted,
	})
	report.Add("state", state.Workspace.Name, "captured", "")

	body, err := wsbundle.EncodeInfo(info)
	if err != nil {
		b.Report = *report
		s.fail(b, err)
		return nil
	}
	if err := store.Put(ctx, wsbundle.InfoObject(prefix, b.Ref), body); err != nil {
		b.Report = *report
		s.fail(b, fmt.Errorf("upload bundle info: %w", err))
		return nil
	}

	for _, a := range info.Artifacts {
		if a.OK() {
			b.Artifacts++
			b.SizeBytes += a.SizeBytes
		}
	}
	b.Report = *report
	s.finish(b)
	return nil
}

// A failed dump is recorded, not fatal: one unreachable database must not cost every other one.
func (s *Service) exportDatabases(ctx context.Context, b *models.WorkspaceBundle, cfg *backup.S3Config, prefix, passphrase string, report *models.BundleReport) []wsbundle.Artifact {
	path := wsbundle.DatabasePath(prefix, b.Ref)
	instances, err := s.Database.List(b.WorkspaceID)
	if err != nil {
		report.Add("database", "", "failed", "could not list databases: "+err.Error())
		return nil
	}
	var out []wsbundle.Artifact
	for i := range instances {
		inst := &instances[i]
		dbs, err := s.Database.ListDatabases(b.WorkspaceID, inst.ID)
		if err != nil {
			report.Add("database", inst.Name, "failed", err.Error())
			continue
		}
		if len(dbs) == 0 {
			// No logical database for the dump tools to read; its declaration still travels.
			report.Add("database", inst.Name, "skipped", "engine "+string(inst.Engine)+" has no logical database to dump")
			continue
		}
		for j := range dbs {
			db := &dbs[j]
			art := wsbundle.Artifact{
				Subject: wsbundle.SubjectDatabase, Instance: inst.Name, Database: db.Name,
				Engine: string(inst.Engine), Path: path,
			}
			dest := backup.Destination{
				Type:          "s3",
				S3:            withPath(cfg, path),
				GPGPassphrase: passphrase,
			}
			rec, err := s.Backup.Run(ctx, inst, db, backup.RunOptions{Trigger: "bundle"}, dest)
			switch {
			case err != nil:
				art.Error = err.Error()
			case rec == nil || rec.Status != models.BackupCompleted:
				art.Error = "dump did not complete"
				if rec != nil && rec.Error != "" {
					art.Error = rec.Error
				}
			default:
				art.File = rec.Filename
				art.SizeBytes = rec.SizeBytes
				art.Encrypted = backup.IsEncryptedArtifact(rec.Filename)
			}
			if art.OK() {
				report.Add("database", inst.Name+"/"+db.Name, "captured", "")
			} else {
				report.Add("database", inst.Name+"/"+db.Name, "failed", art.Error)
			}
			out = append(out, art)
		}
	}
	return out
}

func (s *Service) exportVolumes(ctx context.Context, b *models.WorkspaceBundle, cfg *backup.S3Config, prefix, passphrase string, report *models.BundleReport) []wsbundle.Artifact {
	path := wsbundle.VolumePath(prefix, b.Ref)
	vols, err := s.Volume.List(b.WorkspaceID)
	if err != nil {
		report.Add("volume", "", "failed", "could not list volumes: "+err.Error())
		return nil
	}
	var out []wsbundle.Artifact
	for i := range vols {
		v := &vols[i]
		art := wsbundle.Artifact{Subject: wsbundle.SubjectVolume, Volume: v.Name, Path: path}
		name, encrypted, err := s.archiveVolume(ctx, v, cfg, path, passphrase)
		if err != nil {
			art.Error = err.Error()
			report.Add("volume", v.Name, "failed", err.Error())
		} else {
			art.File = name
			art.Encrypted = encrypted
			report.Add("volume", v.Name, "captured", "")
		}
		out = append(out, art)
	}
	return out
}

// objectLister is the part of blob.Store that sizeArtifacts needs.
type objectLister interface {
	List(ctx context.Context, prefix string) ([]blob.Object, error)
}

// volume-bkup prints only the archive name, so sizes are read from the bucket.
func sizeArtifacts(ctx context.Context, store objectLister, arts []wsbundle.Artifact) {
	listed := map[string][]blob.Object{}
	for i := range arts {
		a := &arts[i]
		if !a.OK() || a.SizeBytes > 0 {
			continue
		}
		objs, ok := listed[a.Path]
		if !ok {
			objs, _ = store.List(ctx, a.Path)
			listed[a.Path] = objs
		}
		for _, o := range objs {
			if o.Key == a.File || strings.HasSuffix(o.Key, "/"+a.File) {
				a.SizeBytes = o.Size
				break
			}
		}
	}
}

func withPath(cfg *backup.S3Config, path string) *backup.S3Config {
	out := *cfg
	out.Path = path
	return &out
}
