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

package platformbackup

import (
	"context"
	"fmt"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

type DBBackupRunner interface {
	Run(ctx context.Context, inst *models.DatabaseInstance, db *models.Database, opts backup.RunOptions, dest backup.Destination) (*models.Backup, error)
	Restore(ctx context.Context, inst *models.DatabaseInstance, db *models.Database, spec backup.RestoreSpec) error
}

// States once which workspaces, databases and volumes count as tenant data; dumps delegate to the backup service.
type RepoTenantSource struct {
	workspaces *repositories.WorkspaceRepository
	databases  *repositories.DatabaseRepository
	volumes    *repositories.VolumeRepository
	runner     DBBackupRunner
}

// One call so every composition root wires it identically; forgetting it fails only at backup time.
func (s *Service) EnableTenantCapture(db *gorm.DB, runner DBBackupRunner) {
	s.SetTenantSource(NewRepoTenantSource(
		repositories.NewWorkspaceRepository(db),
		repositories.NewDatabaseRepository(db),
		repositories.NewVolumeRepository(db),
		runner,
	))
}

// NewRepoTenantSource builds the repository-backed tenant source.
func NewRepoTenantSource(
	workspaces *repositories.WorkspaceRepository,
	databases *repositories.DatabaseRepository,
	volumes *repositories.VolumeRepository,
	runner DBBackupRunner,
) *RepoTenantSource {
	return &RepoTenantSource{workspaces: workspaces, databases: databases, volumes: volumes, runner: runner}
}

// Redis is skipped: always a cache here, with no dump tooling, so capturing it would restore nothing.
func (s *RepoTenantSource) ListTenantDatabases() ([]TenantDatabase, error) {
	workspaces, err := s.workspaces.ListAll()
	if err != nil {
		return nil, err
	}
	var out []TenantDatabase
	for i := range workspaces {
		ws := &workspaces[i]
		instances, err := s.databases.ListByWorkspace(ws.ID)
		if err != nil {
			return nil, err
		}
		for j := range instances {
			inst := &instances[j]
			if inst.Engine == models.DBEngineRedis {
				continue
			}
			dbs, err := s.databases.ListDatabases(inst.ID)
			if err != nil {
				return nil, err
			}
			for k := range dbs {
				out = append(out, TenantDatabase{
					WorkspaceID: ws.ID,
					Workspace:   ws.Name,
					Instance:    inst,
					Database:    &dbs[k],
				})
			}
		}
	}
	return out, nil
}

// ListTenantVolumes returns every workspace-owned volume.
func (s *RepoTenantSource) ListTenantVolumes() ([]TenantVolume, error) {
	vols, err := s.volumes.ListAll()
	if err != nil {
		return nil, err
	}
	workspaces, err := s.workspaces.ListAll()
	if err != nil {
		return nil, err
	}
	slugs := make(map[uint]string, len(workspaces))
	for i := range workspaces {
		slugs[workspaces[i].ID] = workspaces[i].Name
	}

	out := make([]TenantVolume, 0, len(vols))
	for i := range vols {
		v := &vols[i]
		if v.DockerName == "" || v.WorkspaceID == 0 {
			continue
		}
		out = append(out, TenantVolume{
			WorkspaceID: v.WorkspaceID,
			Workspace:   slugs[v.WorkspaceID],
			Name:        v.DockerName,
			ServerID:    v.ServerID,
		})
	}
	return out, nil
}

// BackupTenantDatabase dumps one database to the platform's own destination.
func (s *RepoTenantSource) BackupTenantDatabase(ctx context.Context, td TenantDatabase, dest backup.Destination) (*models.Backup, error) {
	if s.runner == nil {
		return nil, fmt.Errorf("no database backup runner is wired")
	}
	return s.runner.Run(ctx, td.Instance, td.Database, backup.RunOptions{Trigger: "platform-dr"}, dest)
}

// Force drops and recreates first: layering a dump over a freshly seeded DB gives constraint violations.
func (s *RepoTenantSource) RestoreTenantDatabase(ctx context.Context, td TenantDatabase, dest backup.Destination, filename string) error {
	if s.runner == nil {
		return fmt.Errorf("no database backup runner is wired")
	}
	path := ""
	if dest.S3 != nil {
		path = dest.S3.Path
	}
	return s.runner.Restore(ctx, td.Instance, td.Database, backup.RestoreSpec{
		Filename:      filename,
		Destination:   dest.Type,
		S3:            dest.S3,
		S3Path:        path,
		GPGPassphrase: dest.GPGPassphrase,
		Force:         true,
	})
}
