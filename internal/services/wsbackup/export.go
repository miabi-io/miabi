// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// runExport writes the workspace to the bucket as a bundle. Order matters and is the reverse of what it looks
// like: the data artifacts are captured first and the index written last, so a bundle's info file exists only
// once the things it lists do. An interrupted export leaves orphaned objects, not an index promising a restore.
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

	// Preflight the bucket before doing any work. An export dumps every database and archives every volume before
	// it writes its own two objects, so a target that refuses the control plane would fail at the very end — after
	// the expensive part, and after leaving those artifacts behind. One small object answers that in a second.
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
		Encrypted:     true,
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

	sealed, err := wsbundle.Seal(state, passphrase)
	if err != nil {
		b.Report = *report
		s.fail(b, err)
		return nil
	}

	s.phase(b, models.BundlePhaseDatabases)
	info.Artifacts = append(info.Artifacts, s.exportDatabases(ctx, b, cfg, prefix, passphrase, report)...)

	s.phase(b, models.BundlePhaseVolumes)
	info.Artifacts = append(info.Artifacts, s.exportVolumes(ctx, b, cfg, prefix, passphrase, report)...)

	s.phase(b, models.BundlePhaseUpload)
	stateKey := wsbundle.StateObject(prefix, b.Ref)
	if err := store.Put(ctx, stateKey, sealed); err != nil {
		b.Report = *report
		s.fail(b, fmt.Errorf("upload state file: %w", err))
		return nil
	}
	info.Artifacts = append(info.Artifacts, wsbundle.Artifact{
		Subject:   wsbundle.SubjectState,
		File:      "state-" + b.Ref + wsbundle.StateExt,
		Path:      wsbundle.Root(prefix, b.Ref),
		SizeBytes: int64(len(sealed)),
		Encrypted: true,
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

// exportDatabases dumps every logical database in the workspace into the bundle's branch, reusing services/backup
// so each engine is dumped by the tool that understands it. A dump that fails does not fail the bundle — it is
// recorded on the artifact and in the report. One unreachable database must not cost every other one.
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
			// Redis and friends: no logical database, so nothing the dump tools can
			// read. Its declaration still travels; its contents do not.
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
			rec, err := s.Backup.Run(ctx, inst, db, "bundle", dest)
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
				art.Encrypted = strings.HasSuffix(rec.Filename, ".gpg")
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

// exportVolumes archives every workspace volume into the bundle's branch with the
// volume-bkup helper, on the node that actually holds each volume.
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

func withPath(cfg *backup.S3Config, path string) *backup.S3Config {
	out := *cfg
	out.Path = path
	return &out
}
