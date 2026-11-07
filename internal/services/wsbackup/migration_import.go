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
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/wsbundle"
)

// Errors an import returns before it starts.
var (
	ErrSourceWorkspace = errors.New("the migration key does not open that workspace on the source")
	ErrBundleTooNew    = errors.New("the source writes a newer workspace format than this Miabi reads: upgrade this install first")
)

// exportPoll is how often an import checks on the source's export, and exportWait how long it waits.
const (
	exportPoll = 3 * time.Second
	exportWait = 12 * time.Hour
)

// MigrationSource is how an importer reaches the source install.
type MigrationSource struct {
	// Code is the migration code the source's owner copied; it stands for URL and Key.
	Code        string `json:"code"`
	URL         string `json:"source_url"`
	Key         string `json:"key"`
	InsecureTLS bool   `json:"insecure_skip_tls_verify"`
	CABundle    string `json:"ca_bundle"`
}

// MigrationImportInput is one workspace to pull from a source install.
type MigrationImportInput struct {
	Source MigrationSource
	// SourceWorkspace is the workspace's name on the source; empty when the key opens exactly one.
	SourceWorkspace string
	// NewWorkspaceName is the workspace created here; the source's name when empty.
	NewWorkspaceName string
	RestoreData      bool
	DeployApps       bool
	// Freeze stops the source's apps and refuses changes there before the copy, for a consistent cut-over.
	Freeze bool
	// Rehearse moves the configuration only, without data, freeze or deploy, to check the move first.
	Rehearse bool
	// RetireSource marks the source workspace as migrated here once the import succeeds.
	RetireSource bool
	BatchID      string
}

func (s *Service) ProbeMigrationSource(ctx context.Context, src MigrationSource) (*MigrationSourceInfo, error) {
	codeWS, err := src.resolve()
	if err != nil {
		return nil, err
	}
	c, err := newMigrationClient(src.URL, src.Key, src.InsecureTLS, src.CABundle)
	if err != nil {
		return nil, err
	}
	info, err := c.info(ctx)
	if err != nil {
		return nil, err
	}
	if info.Schema > wsbundle.StateSchema {
		return nil, ErrBundleTooNew
	}
	info.CodeWorkspace = codeWS
	return info, nil
}

// Created here rather than on the worker so a taken name is an error the caller sees at once.
func (s *Service) StartMigrationImport(ctx context.Context, userID uint, in MigrationImportInput) (*models.WorkspaceBundle, error) {
	codeWS, err := in.Source.resolve()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.SourceWorkspace) == "" {
		in.SourceWorkspace = codeWS
	}
	in.Source.Code = ""
	info, err := s.ProbeMigrationSource(ctx, in.Source)
	if err != nil {
		return nil, err
	}
	srcWS, err := pickSourceWorkspace(info, in.SourceWorkspace)
	if err != nil {
		return nil, err
	}
	if in.Rehearse {
		in.RestoreData, in.DeployApps, in.Freeze, in.RetireSource = false, false, false, false
	}
	name := strings.TrimSpace(in.NewWorkspaceName)
	if name == "" {
		name = srcWS.Name
	}
	display := srcWS.DisplayName
	if display == "" || strings.TrimSpace(in.NewWorkspaceName) != "" {
		display = name
	}
	keyEnc, err := crypto.Encrypt(strings.TrimSpace(in.Source.Key))
	if err != nil {
		return nil, err
	}
	ws, err := s.Workspace.Create(userID, name, display, "Migrated from "+sourceHost(in.Source.URL))
	if err != nil {
		return nil, err
	}
	actor := userID
	return s.start(ctx, &models.WorkspaceBundle{
		WorkspaceID:       ws.ID,
		TargetWorkspaceID: ws.ID,
		Kind:              models.BundleMigrationImport,
		Ref:               fmt.Sprintf("mbmig_%s_from_%s", srcWS.Name, sourceHost(in.Source.URL)),
		Status:            models.BackupPending,
		Trigger:           "migration",
		RestoreData:       in.RestoreData,
		DeployApps:        in.DeployApps,
		SourceWorkspace:   srcWS.Name,
		Migration: &models.BundleMigration{
			SourceURL: strings.TrimRight(strings.TrimSpace(in.Source.URL), "/"), SourceVersion: info.Version,
			InsecureTLS: in.Source.InsecureTLS, Freeze: in.Freeze, Rehearse: in.Rehearse,
			RetireSource: in.RetireSource, BatchID: in.BatchID, Schema: info.Schema,
		},
		SecretEnc: keyEnc,
		CreatedBy: &actor,
	})
}

func pickSourceWorkspace(info *MigrationSourceInfo, want string) (*MigrationWorkspace, error) {
	want = strings.TrimSpace(want)
	if want == "" {
		if len(info.Workspaces) == 1 {
			return &info.Workspaces[0], nil
		}
		return nil, fmt.Errorf("the key opens %d workspaces on the source: name the one to migrate", len(info.Workspaces))
	}
	for i := range info.Workspaces {
		if info.Workspaces[i].Name == want {
			return &info.Workspaces[i], nil
		}
	}
	return nil, ErrSourceWorkspace
}

func sourceHost(raw string) string {
	if u, err := url.Parse(strings.TrimSpace(raw)); err == nil && u.Host != "" {
		return u.Host
	}
	return "another Miabi"
}

func (s *Service) runMigrationImport(ctx context.Context, b *models.WorkspaceBundle) error {
	m := b.Migration
	if m == nil {
		s.fail(b, errors.New("the import has no source"))
		return nil
	}
	key, err := crypto.Decrypt(b.SecretEnc)
	if err != nil {
		s.fail(b, fmt.Errorf("read the migration key: %w", err))
		return nil
	}
	// The CA bundle is not kept: an import that needs one is retried with it, rather than storing it.
	c, err := newMigrationClient(m.SourceURL, key, m.InsecureTLS, "")
	if err != nil {
		s.fail(b, err)
		return nil
	}
	report := &models.BundleReport{}

	s.phase(b, models.BundlePhaseTransfer)
	exp, err := c.createExport(ctx, b.SourceWorkspace, m.Freeze, b.RestoreData)
	if err != nil {
		s.failImport(ctx, b, c, report, fmt.Errorf("start the export on the source: %w", err))
		return nil
	}
	m.RemoteExportID = exp.ID
	_ = s.Repo.Update(b)
	if exp, err = s.awaitExport(ctx, c, b.SourceWorkspace, exp.ID); err != nil {
		s.failImport(ctx, b, c, report, err)
		return nil
	}
	report.Add("migration", b.SourceWorkspace, "captured", fmt.Sprintf("exported by %s (%s), %d artifact(s)",
		sourceHost(m.SourceURL), m.SourceVersion, len(exp.Artifacts)))

	body, err := c.state(ctx, b.SourceWorkspace, exp.ID)
	if err != nil {
		s.failImport(ctx, b, c, report, fmt.Errorf("read the workspace from the source: %w", err))
		return nil
	}
	state, err := wsbundle.Decode(body, "")
	if err != nil {
		s.failImport(ctx, b, c, report, err)
		return nil
	}

	src := &remoteSource{client: c, workspace: b.SourceWorkspace, exportID: exp.ID, index: map[string]int{}, manifest: exp.Artifacts}
	info := &wsbundle.Info{Ref: b.Ref, Workspace: state.Workspace.Name}
	for i, a := range exp.Artifacts {
		if a.Error != "" {
			report.Add(a.Subject, artifactName(a), "failed", "the source could not export it: "+a.Error)
			continue
		}
		info.Artifacts = append(info.Artifacts, wsbundle.Artifact{
			Subject: a.Subject, Instance: a.Instance, Database: a.Database, Engine: a.Engine,
			Volume: a.Volume, File: a.File, SizeBytes: a.Size,
		})
		src.index[artifactKey(a.Subject, a.Instance, a.Database, a.Volume)] = i
	}

	s.restoreFrom(ctx, b, state, info, src, report)
	s.settleSource(ctx, b, c)
	return nil
}

// awaitExport polls the source until its export is ready or has failed.
func (s *Service) awaitExport(ctx context.Context, c *migrationClient, ws string, id uint) (*MigrationExportView, error) {
	deadline := time.Now().Add(exportWait)
	for {
		exp, err := c.export(ctx, ws, id)
		if err != nil {
			return nil, fmt.Errorf("follow the export on the source: %w", err)
		}
		switch exp.Status {
		case models.BackupCompleted:
			return exp, nil
		case models.BackupFailed, models.BackupPartial:
			return nil, fmt.Errorf("the source could not export the workspace: %s", exp.Error)
		}
		if time.Now().After(deadline) {
			return nil, errors.New("the source's export did not finish in time")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(exportPoll):
		}
	}
}

// failImport fails an import before anything was restored, undoing the source's freeze so it serves again.
func (s *Service) failImport(ctx context.Context, b *models.WorkspaceBundle, c *migrationClient, report *models.BundleReport, cause error) {
	m := b.Migration
	if m.RemoteExportID != 0 {
		if m.Freeze {
			if err := c.action(ctx, b.SourceWorkspace, m.RemoteExportID, "unfreeze", nil); err == nil {
				m.SourceUnfrozen = true
				report.Note("The source workspace was unfrozen: it serves and accepts changes again.")
			}
		}
		_ = c.deleteExport(ctx, b.SourceWorkspace, m.RemoteExportID)
	}
	b.Report = *report
	s.fail(b, cause)
}

func (s *Service) settleSource(ctx context.Context, b *models.WorkspaceBundle, c *migrationClient) {
	m := b.Migration
	host := sourceHost(m.SourceURL)
	target := s.PublicURL
	if target == "" {
		target = "another Miabi"
	}
	switch {
	case b.Status == models.BackupFailed && m.Freeze:
		if err := c.action(ctx, b.SourceWorkspace, m.RemoteExportID, "unfreeze", nil); err == nil {
			m.SourceUnfrozen = true
			b.Report.Notes = append(b.Report.Notes, "The import failed, so the source workspace was unfrozen and serves again.")
		}
	case m.RetireSource && b.Status == models.BackupCompleted:
		if err := c.action(ctx, b.SourceWorkspace, m.RemoteExportID, "retire", map[string]string{"target": target}); err == nil {
			m.SourceRetired = true
			b.Report.Notes = append(b.Report.Notes, "The source workspace on "+host+" is marked as migrated here and stays frozen.")
		} else {
			b.Report.Notes = append(b.Report.Notes, "The source workspace could not be marked as migrated: "+err.Error())
		}
	case m.Freeze || m.RetireSource:
		if m.RetireSource {
			b.Report.Notes = append(b.Report.Notes, "The source workspace was not marked as migrated: not everything came across.")
		}
		if m.Freeze {
			b.Report.Notes = append(b.Report.Notes, "The source workspace on "+host+" is still frozen. Retire it once this one serves, or unfreeze it there to roll back.")
		}
	}
	if m.Rehearse {
		b.Report.Notes = append(b.Report.Notes, "This was a rehearsal: configuration only. Delete this workspace, then run the migration with data.")
	}
	if err := c.deleteExport(ctx, b.SourceWorkspace, m.RemoteExportID); err != nil {
		logger.Warn("migration import: could not purge the source's export", "export", m.RemoteExportID, "error", err)
	}
	// Last because it closes the migration and the code every call above needs; only a completed import ends it.
	if b.Status == models.BackupCompleted && !m.Rehearse {
		if err := c.action(ctx, b.SourceWorkspace, m.RemoteExportID, "complete", map[string]string{"target": target}); err != nil {
			logger.Warn("migration import: could not tell the source it completed", "export", m.RemoteExportID, "error", err)
		}
	}
	_ = s.Repo.Update(b)
}

func artifactKey(subject, instance, database, volume string) string {
	return subject + "\x00" + instance + "\x00" + database + "\x00" + volume
}

func artifactName(a models.MigrationArtifact) string {
	if a.Subject == wsbundle.SubjectVolume {
		return a.Volume
	}
	return a.Instance + "/" + a.Database
}

// A temp file lets a dropped download resume and the checksum be verified before anything loads.
type remoteSource struct {
	client    *migrationClient
	workspace string
	exportID  uint
	index     map[string]int
	manifest  []models.MigrationArtifact
}

func (rs *remoteSource) fetch(ctx context.Context, art wsbundle.Artifact) (*os.File, models.MigrationArtifact, func(), error) {
	n, ok := rs.index[artifactKey(art.Subject, art.Instance, art.Database, art.Volume)]
	if !ok {
		return nil, models.MigrationArtifact{}, nil, ErrNoArtifact
	}
	a := rs.manifest[n]
	f, err := os.CreateTemp("", "miabi-migration-*")
	if err != nil {
		return nil, a, nil, err
	}
	done := func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}
	if err := rs.client.download(ctx, rs.workspace, rs.exportID, n, a, f); err != nil {
		done()
		return nil, a, nil, err
	}
	return f, a, done, nil
}

func (rs *remoteSource) restoreDatabase(ctx context.Context, r *restoreRun, art wsbundle.Artifact, inst *models.DatabaseInstance, db *models.Database) error {
	f, a, done, err := rs.fetch(ctx, art)
	if err != nil {
		return err
	}
	defer done()
	return r.svc.Backup.RestoreFile(ctx, inst, db, a.File, f, a.Size)
}

func (rs *remoteSource) restoreVolume(ctx context.Context, r *restoreRun, art wsbundle.Artifact, vol *models.Volume) error {
	f, a, done, err := rs.fetch(ctx, art)
	if err != nil {
		return err
	}
	defer done()
	dc, err := r.svc.Clients.For(vol.ServerID)
	if err != nil {
		return err
	}
	image := r.svc.volImage()
	if err := dc.PullImage(ctx, image, nil); err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	staging := fmt.Sprintf("mb-migration-in-%d-%d", r.bundle.ID, vol.ID)
	if _, err := dc.CreateVolume(ctx, staging, map[string]string{docker.LabelWorkspace: fmt.Sprint(vol.WorkspaceID)}, 0); err != nil {
		return fmt.Errorf("create staging volume: %w", err)
	}
	defer func() { _ = dc.RemoveVolume(context.Background(), staging, true) }()
	if err := dc.CopyToVolume(ctx, staging, image, a.File, f, a.Size); err != nil {
		return fmt.Errorf("stage the archive: %w", err)
	}
	exit, out, err := dc.RunOneShot(ctx, docker.RunSpec{
		Name:   oneShotName("mb-mig-volrestore", vol.ID),
		Image:  image,
		Cmd:    []string{"restore", "--storage", "local", "--file", a.File},
		Mounts: map[string]string{vol.DockerName: volumeMount, staging: "/backup"},
	})
	if err != nil {
		return err
	}
	if exit != 0 {
		return fmt.Errorf("volume restore exited %d: %s", exit, tail(out))
	}
	return nil
}
