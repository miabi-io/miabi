// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// DBBackupRunner is the slice of services/backup platform DR needs: dump and
// restore a managed database against a destination it chooses.
type DBBackupRunner interface {
	Run(ctx context.Context, inst *models.DatabaseInstance, db *models.Database, trigger string, dest backup.Destination) (*models.Backup, error)
	Restore(ctx context.Context, inst *models.DatabaseInstance, db *models.Database, spec backup.RestoreSpec) error
}

// RepoTenantSource is the production TenantSource: it walks the workspace, database and volume
// repositories and delegates the actual dump to the existing backup service. It lives here so the
// traversal — which workspaces, databases and volumes count as tenant data — is stated once.
type RepoTenantSource struct {
	workspaces *repositories.WorkspaceRepository
	databases  *repositories.DatabaseRepository
	volumes    *repositories.VolumeRepository
	runner     DBBackupRunner
}

// EnableTenantCapture wires tenant capture from a database handle: one call instead of four constructor
// arguments, because it must be done identically in every composition root. A root that forgets it does
// not fail at startup — it fails at backup time, with "no tenant source is wired".
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

// ListTenantDatabases returns every logical database on every managed instance. Redis is skipped: it is a
// cache in every Miabi topology, the backup tooling has no dump for it, and pretending to capture it would
// put a reassuring line in a recovery point that restores nothing.
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
	return s.runner.Run(ctx, td.Instance, td.Database, "platform-dr", dest)
}

// RestoreTenantDatabase loads a dump back into a live database instance. Force is set, so the database is
// dropped and recreated first: a DR restore lands in a database the platform just provisioned, and layering
// a dump over what a fresh instance seeded gives constraint violations that look like a corrupt backup.
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
