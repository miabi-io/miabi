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
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/wsbundle"
)

// MigrationExportTTL is how long a migration export's staged data waits to be collected.
const MigrationExportTTL = 24 * time.Hour

// Errors a migration export returns to the importing install.
var (
	ErrNotMigrationExport = errors.New("not a migration export")
	ErrExportNotFound     = errors.New("no such migration export for this workspace")
	ErrExportNotReady     = errors.New("the export is not ready")
	ErrExportPurged       = errors.New("the export's staged data was purged; start a new export")
	ErrNoArtifact         = errors.New("no such artifact in this export")
)

// Freezer is the restriction service's migration freeze.
type Freezer interface {
	Freeze(ctx context.Context, wsID, actorID uint, reason string) error
	Unfreeze(ctx context.Context, wsID uint) error
	SetFrozenReason(wsID uint, reason string) error
}

// MigrationExportInput is what an importing install asks of the source.
type MigrationExportInput struct {
	// Freeze stops the workspace's apps and refuses changes before anything is collected, so the copy
	// is consistent. The workspace stays frozen until it is retired or unfrozen.
	Freeze bool
	// Data dumps the databases and archives the volumes; without it only the configuration travels.
	Data bool
}

// StartMigrationExport stages a workspace for another install to pull, on the worker.
func (s *Service) StartMigrationExport(ctx context.Context, workspaceID, keyID, actorID uint, in MigrationExportInput) (*models.WorkspaceBundle, error) {
	ws, err := s.Workspaces.FindByID(workspaceID)
	if err != nil {
		return nil, err
	}
	exp := time.Now().Add(MigrationExportTTL)
	actor := actorID
	return s.start(ctx, &models.WorkspaceBundle{
		WorkspaceID:     workspaceID,
		Kind:            models.BundleMigrationExport,
		Ref:             fmt.Sprintf("mbmig_%s_%s", ws.Name, time.Now().UTC().Format("20060102T150405Z")),
		Status:          models.BackupPending,
		Trigger:         "migration",
		RestoreData:     in.Data,
		SourceWorkspace: ws.Name,
		Migration:       &models.BundleMigration{KeyID: keyID, Freeze: in.Freeze, Data: in.Data, Schema: wsbundle.StateSchema},
		ExpiresAt:       &exp,
		CreatedBy:       &actor,
	})
}

func (s *Service) runMigrationExport(ctx context.Context, b *models.WorkspaceBundle) error {
	m := b.Migration
	if m == nil {
		m = &models.BundleMigration{}
		b.Migration = m
	}
	report := &models.BundleReport{}
	if m.Freeze {
		s.phase(b, models.BundlePhaseFreeze)
		if s.Freezer == nil {
			s.fail(b, errors.New("this install cannot freeze a workspace"))
			return nil
		}
		actor := uint(0)
		if b.CreatedBy != nil {
			actor = *b.CreatedBy
		}
		if err := s.Freezer.Freeze(ctx, b.WorkspaceID, actor, "migrating to another Miabi"); err != nil {
			s.fail(b, fmt.Errorf("freeze: %w", err))
			return nil
		}
		report.Add("workspace", b.SourceWorkspace, "captured", "frozen: apps stopped and changes refused until it is retired or unfrozen")
	}

	s.phase(b, models.BundlePhaseState)
	state, err := s.collect(b.WorkspaceID, report)
	if err != nil {
		b.Report = *report
		s.failExport(ctx, b, err)
		return nil
	}
	body, err := wsbundle.Encode(state, "")
	if err != nil {
		s.failExport(ctx, b, err)
		return nil
	}
	enc, err := crypto.EncryptWS(b.WorkspaceID, string(body))
	if err != nil {
		s.failExport(ctx, b, err)
		return nil
	}
	b.SecretEnc = enc

	if m.Data {
		s.phase(b, models.BundlePhaseDatabases)
		m.Manifest = append(m.Manifest, s.stageDatabases(ctx, b, report)...)
		s.phase(b, models.BundlePhaseVolumes)
		m.Manifest = append(m.Manifest, s.stageVolumes(ctx, b, report)...)
	}
	b.SizeBytes = int64(len(body))
	b.Artifacts = 0
	for _, a := range m.Manifest {
		if a.Error == "" {
			b.Artifacts++
			b.SizeBytes += a.Size
		}
	}
	report.Add("state", state.Workspace.Name, "captured", "")
	b.Report = *report
	s.finish(b)
	return nil
}

// failExport fails an export and undoes its freeze: with nothing to import, the source must serve again.
func (s *Service) failExport(ctx context.Context, b *models.WorkspaceBundle, cause error) {
	if b.Migration != nil && b.Migration.Freeze && s.Freezer != nil {
		if err := s.Freezer.Unfreeze(ctx, b.WorkspaceID); err != nil {
			logger.Warn("migration export: could not unfreeze after a failure", "workspace", b.WorkspaceID, "error", err)
		}
	}
	s.purgeStaged(ctx, b)
	s.fail(b, cause)
}

// stageDatabases dumps every logical database to the workspace's local backup volume, on its own node.
func (s *Service) stageDatabases(ctx context.Context, b *models.WorkspaceBundle, report *models.BundleReport) []models.MigrationArtifact {
	instances, err := s.Database.List(b.WorkspaceID)
	if err != nil {
		report.Add("database", "", "failed", "could not list databases: "+err.Error())
		return nil
	}
	var out []models.MigrationArtifact
	for i := range instances {
		inst := &instances[i]
		dbs, err := s.Database.ListDatabases(b.WorkspaceID, inst.ID)
		if err != nil {
			report.Add("database", inst.Name, "failed", err.Error())
			continue
		}
		if len(dbs) == 0 {
			report.Add("database", inst.Name, "skipped", "engine "+string(inst.Engine)+" has no logical database to dump")
			continue
		}
		for j := range dbs {
			db := &dbs[j]
			art := models.MigrationArtifact{Subject: wsbundle.SubjectDatabase, Instance: inst.Name, Database: db.Name, Engine: string(inst.Engine)}
			rec, err := s.Backup.Run(ctx, inst, db, backup.RunOptions{Trigger: "migration"}, backup.Destination{Type: "local"})
			switch {
			case err != nil:
				art.Error = err.Error()
			case rec == nil || rec.Status != models.BackupCompleted:
				art.Error = "dump did not complete"
				if rec != nil && rec.Error != "" {
					art.Error = rec.Error
				}
			default:
				art.File, art.Size, art.SHA256 = rec.Filename, rec.SizeBytes, rec.Checksum
				art.ServerID, art.DockerVolume, art.BackupID = rec.ServerID, rec.VolumeName, rec.ID
			}
			if rec != nil && art.BackupID == 0 {
				art.BackupID = rec.ID
			}
			reportArtifact(report, "database", inst.Name+"/"+db.Name, art.Error)
			out = append(out, art)
		}
	}
	return out
}

// stagingVolume is where a migration export archives volumes, one per node.
func stagingVolume(bundleID uint) string { return fmt.Sprintf("mb-migration-%d", bundleID) }

// stageVolumes archives every workspace volume into a staging volume on the node that holds it.
func (s *Service) stageVolumes(ctx context.Context, b *models.WorkspaceBundle, report *models.BundleReport) []models.MigrationArtifact {
	vols, err := s.Volume.List(b.WorkspaceID)
	if err != nil {
		report.Add("volume", "", "failed", "could not list volumes: "+err.Error())
		return nil
	}
	var out []models.MigrationArtifact
	for i := range vols {
		v := &vols[i]
		art := models.MigrationArtifact{Subject: wsbundle.SubjectVolume, Volume: v.Name, ServerID: v.ServerID, DockerVolume: stagingVolume(b.ID)}
		if err := s.archiveVolumeLocal(ctx, v, &art); err != nil {
			art.Error = err.Error()
		}
		reportArtifact(report, "volume", v.Name, art.Error)
		out = append(out, art)
	}
	return out
}

func (s *Service) archiveVolumeLocal(ctx context.Context, v *models.Volume, art *models.MigrationArtifact) error {
	dc, err := s.Clients.For(v.ServerID)
	if err != nil {
		return err
	}
	image := s.volImage()
	if err := dc.PullImage(ctx, image, nil); err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	if _, err := dc.CreateVolume(ctx, art.DockerVolume, map[string]string{docker.LabelWorkspace: fmt.Sprint(v.WorkspaceID)}, 0); err != nil {
		return fmt.Errorf("create staging volume: %w", err)
	}
	exit, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:   oneShotName("mb-mig-volbkup", v.ID),
		Image:  image,
		Env:    []string{backup.IndexEnv(v.Name)},
		Cmd:    []string{"backup", "--storage", "local", "--name", v.Name},
		Mounts: map[string]string{v.DockerName: volumeMount, art.DockerVolume: "/backup"},
	})
	if err != nil {
		return err
	}
	if exit != 0 {
		return fmt.Errorf("volume archive exited %d: %s", exit, tail(out))
	}
	name, _, err := backup.ArtifactName(out, volArtifactRe)
	if err != nil {
		return err
	}
	art.File, art.Size, art.SHA256 = name, backup.ArtifactSize(out), backup.ArtifactSHA256(out)
	return nil
}

func reportArtifact(report *models.BundleReport, kind, name, errText string) {
	if errText == "" {
		report.Add(kind, name, "captured", "")
		return
	}
	report.Add(kind, name, "failed", errText)
}

// MigrationExport loads an export for the importing install, refusing anything else or a purged one.
func (s *Service) MigrationExport(workspaceID, id uint) (*models.WorkspaceBundle, error) {
	b, err := s.Repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrExportNotFound
	}
	if b.Kind != models.BundleMigrationExport || b.Migration == nil {
		return nil, ErrNotMigrationExport
	}
	return b, nil
}

// MigrationState returns an export's state document, unencrypted: the transport is what protects it.
func (s *Service) MigrationState(b *models.WorkspaceBundle) ([]byte, error) {
	if err := readyExport(b); err != nil {
		return nil, err
	}
	plain, err := crypto.Decrypt(b.SecretEnc)
	if err != nil {
		return nil, err
	}
	return []byte(plain), nil
}

// OpenMigrationArtifact streams one staged artifact from the node that holds it.
func (s *Service) OpenMigrationArtifact(ctx context.Context, b *models.WorkspaceBundle, index int) (io.ReadCloser, int64, *models.MigrationArtifact, error) {
	if err := readyExport(b); err != nil {
		return nil, 0, nil, err
	}
	if index < 0 || index >= len(b.Migration.Manifest) || b.Migration.Manifest[index].Error != "" {
		return nil, 0, nil, ErrNoArtifact
	}
	a := &b.Migration.Manifest[index]
	if a.Subject == wsbundle.SubjectDatabase {
		rec, err := s.Backup.Get(b.WorkspaceID, a.BackupID)
		if err != nil {
			return nil, 0, nil, ErrNoArtifact
		}
		rc, size, _, err := s.Backup.Download(ctx, rec)
		return rc, size, a, err
	}
	dc, err := s.Clients.For(a.ServerID)
	if err != nil {
		return nil, 0, nil, err
	}
	rc, size, err := dc.CopyFileFromVolume(ctx, a.DockerVolume, s.volImage(), a.File)
	return rc, size, a, err
}

func readyExport(b *models.WorkspaceBundle) error {
	switch {
	case b.Migration == nil:
		return ErrNotMigrationExport
	case b.Migration.Purged:
		return ErrExportPurged
	case b.Status != models.BackupCompleted:
		return ErrExportNotReady
	}
	return nil
}

// PurgeMigrationExport removes an export's staged dumps and archives and forgets its state. The
// importing install calls it once it has what it needs; the sweep catches the ones it never collects.
func (s *Service) PurgeMigrationExport(ctx context.Context, b *models.WorkspaceBundle) error {
	if b.Migration == nil || b.Migration.Purged {
		return nil
	}
	s.purgeStaged(ctx, b)
	b.SecretEnc = ""
	b.Migration.Purged = true
	return s.Repo.Update(b)
}

func (s *Service) purgeStaged(ctx context.Context, b *models.WorkspaceBundle) {
	if b.Migration == nil {
		return
	}
	volumes := map[uint]string{}
	for _, a := range b.Migration.Manifest {
		switch {
		case a.Subject == wsbundle.SubjectDatabase && a.BackupID != 0:
			if rec, err := s.Backup.Get(b.WorkspaceID, a.BackupID); err == nil {
				if err := s.Backup.Delete(ctx, rec); err != nil {
					logger.Warn("migration export: could not delete a staged dump", "backup", rec.ID, "error", err)
				}
			}
		case a.Subject == wsbundle.SubjectVolume && a.DockerVolume != "":
			volumes[a.ServerID] = a.DockerVolume
		}
	}
	for server, name := range volumes {
		dc, err := s.Clients.For(server)
		if err != nil {
			continue
		}
		if err := dc.RemoveVolume(ctx, name, true); err != nil && !strings.Contains(err.Error(), "no such volume") {
			logger.Warn("migration export: could not remove the staging volume", "volume", name, "error", err)
		}
	}
}

// SweepMigrationExports purges exports nobody collected in time. It never unfreezes: whether the
// workspace moved is the owner's call, not a timer's.
func (s *Service) SweepMigrationExports(ctx context.Context) error {
	due, err := s.Repo.ListExpiredMigrationExports(time.Now())
	if err != nil {
		return err
	}
	var errs []error
	for i := range due {
		if err := s.PurgeMigrationExport(ctx, &due[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// RetireMigrationExport records that the workspace now lives on another install. It stays frozen, with
// the reason naming where it went, until its owner deletes or unfreezes it.
func (s *Service) RetireMigrationExport(b *models.WorkspaceBundle, target string) error {
	if s.Freezer == nil {
		return errors.New("this install cannot freeze a workspace")
	}
	reason := "migrated to another Miabi"
	if t := strings.TrimSpace(target); t != "" {
		reason = "migrated to " + t
	}
	return s.Freezer.SetFrozenReason(b.WorkspaceID, reason)
}

// UnfreezeMigration rolls a source back: its apps start again and it accepts changes.
func (s *Service) UnfreezeMigration(ctx context.Context, workspaceID uint) error {
	if s.Freezer == nil {
		return nil
	}
	return s.Freezer.Unfreeze(ctx, workspaceID)
}

// MigrationPreview counts what moving a workspace involves, and roughly how much data.
func (s *Service) MigrationPreview(workspaceID uint) (apps, databases, volumes int, size int64) {
	if list, err := s.App.List(workspaceID); err == nil {
		apps = len(list)
	}
	if insts, err := s.Database.List(workspaceID); err == nil {
		databases = len(insts)
		for i := range insts {
			if dbs, err := s.Database.ListDatabases(workspaceID, insts[i].ID); err == nil {
				for j := range dbs {
					size += dbs[j].SizeBytes
				}
			}
		}
	}
	if vols, err := s.Volume.List(workspaceID); err == nil {
		volumes = len(vols)
		for i := range vols {
			size += vols[i].UsedBytes
		}
	}
	return apps, databases, volumes, size
}

// CompleteMigrationExport records that the importer has the workspace, so the source's migration is over.
func (s *Service) CompleteMigrationExport(b *models.WorkspaceBundle, target string) error {
	if b.Migration == nil {
		return ErrNotMigrationExport
	}
	b.Migration.Completed = true
	b.Migration.ImportedBy = strings.TrimSpace(target)
	return s.Repo.Update(b)
}

// MigrationExports lists a workspace's migration exports, most recent first; all of the install's when
// workspaceID is 0.
func (s *Service) MigrationExports(workspaceID uint, limit int) ([]models.WorkspaceBundle, error) {
	if workspaceID == 0 {
		return s.Repo.ListByKind(models.BundleMigrationExport, limit)
	}
	runs, err := s.Repo.ListByWorkspace(workspaceID, limit)
	if err != nil {
		return nil, err
	}
	out := runs[:0]
	for _, r := range runs {
		if r.Kind == models.BundleMigrationExport {
			out = append(out, r)
		}
	}
	return out, nil
}
