// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/miabi-io/miabi/internal/docker"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

// MoveTarget is an instance's data volume prepared on another node, ahead of moving the instance there.
type MoveTarget struct {
	StorageClass string `json:"storage_class"`
	CreatedAt    string `json:"created_at"`
}

// PrepareMove creates the instance's data volume, under the same name, on another node. The storage class
// is kept when that node registers it and falls back to the data root otherwise. The instance itself is not
// touched; re-running it is harmless.
func (s *Service) PrepareMove(ctx context.Context, inst *models.DatabaseInstance, serverID uint) (MoveTarget, error) {
	dc, err := s.clients.For(serverID)
	if err != nil {
		return MoveTarget{}, err
	}
	at := *inst
	at.ServerID = serverID
	at.StorageClassName = models.DefaultStorageClassName
	if s.storage != nil && inst.StorageClassName != "" && inst.StorageClassName != models.DefaultStorageClassName {
		if class, _, perr := s.storage.PlaceVolume(ctx, inst.WorkspaceID, serverID, inst.StorageClassName, inst.VolumeName); perr == nil {
			at.StorageClassName = class
		}
	}
	dv, err := dc.CreateVolumeWith(ctx, s.dataVolumeSpec(&at))
	if err != nil {
		return MoveTarget{}, fmt.Errorf("create data volume: %w", err)
	}
	return MoveTarget{StorageClass: at.StorageClassName, CreatedAt: dv.CreatedAt}, nil
}

// StopForMove stops the instance's container and keeps it: it is what a rollback starts again.
func (s *Service) StopForMove(ctx context.Context, inst *models.DatabaseInstance) error {
	if inst.ContainerID == "" {
		return nil
	}
	dc, err := s.dockerFor(inst)
	if err != nil {
		return err
	}
	if err := dc.StopContainer(ctx, inst.ContainerID, 30); err != nil {
		return err
	}
	inst.Status = models.DBStatusStopped
	if err := s.repo.Update(inst); err != nil {
		return err
	}
	s.publishStatus(inst)
	return nil
}

// BringUpMoved starts an instance on the node its record now names, over the data copied there, and waits
// until it answers.
func (s *Service) BringUpMoved(ctx context.Context, inst *models.DatabaseInstance) error {
	spec, ok := specs[inst.Engine]
	if !ok {
		return ErrUnsupportedEngine
	}
	adminPass, err := crypto.Decrypt(inst.AdminPasswordEnc)
	if err != nil {
		return err
	}
	if err := s.bringUp(ctx, inst, spec, adminPass); err != nil {
		return err
	}
	if inst.SupportsLogicalDatabases() {
		if err := s.waitReady(ctx, inst); err != nil {
			return err
		}
	}
	s.publishStatus(inst)
	return nil
}

// StartStopped starts an instance's existing container again, as a rollback does for the source it left.
func (s *Service) StartStopped(ctx context.Context, inst *models.DatabaseInstance) error {
	if inst.ContainerID == "" {
		return s.BringUpMoved(ctx, inst)
	}
	dc, err := s.dockerFor(inst)
	if err != nil {
		return err
	}
	if err := dc.StartContainer(ctx, inst.ContainerID); err != nil {
		return err
	}
	inst.Status = models.DBStatusRunning
	if err := s.repo.Update(inst); err != nil {
		return err
	}
	s.publishStatus(inst)
	return nil
}

// DropCopy removes an instance's container and data volume from a node that no longer holds it.
func (s *Service) DropCopy(ctx context.Context, serverID uint, containerID, volumeName, className string) error {
	dc, err := s.clients.For(serverID)
	if err != nil {
		return err
	}
	if containerID != "" {
		_ = dc.StopContainer(ctx, containerID, 10)
		_ = dc.RemoveContainer(ctx, containerID, true)
	}
	if volumeName != "" {
		if err := dc.RemoveVolume(ctx, volumeName, true); err != nil {
			return err
		}
		if s.storage != nil {
			s.storage.ReclaimVolumeDir(ctx, serverID, className, volumeName)
		}
	}
	return nil
}

// FindInstance loads an instance by id, whatever its workspace, for a run that already holds it.
func (s *Service) FindInstance(id uint) (*models.DatabaseInstance, error) { return s.repo.FindByID(id) }

// FindDatabase loads a logical database by id.
func (s *Service) FindDatabase(id uint) (*models.Database, error) { return s.repo.FindDatabaseByID(id) }

// ListByWorkspaceRaw lists a workspace's instances without the live annotations List adds.
func (s *Service) ListByWorkspaceRaw(workspaceID uint) ([]models.DatabaseInstance, error) {
	return s.repo.ListByWorkspace(workspaceID)
}

// ListInstanceDatabases lists an instance's logical databases.
func (s *Service) ListInstanceDatabases(instanceID uint) ([]models.Database, error) {
	return s.repo.ListDatabases(instanceID)
}

// SetDatabaseApp records which application a logical database belongs to (nil for none), keeping its
// prefix, without touching the app's environment: the caller re-injects it.
func (s *Service) SetDatabaseApp(d *models.Database, appID *uint, prefix string) error {
	d.ApplicationID = appID
	d.EnvPrefix = prefix
	return s.repo.UpdateDatabase(d)
}

// DumpSpec is a one-shot container that writes a logical database to stdout, run next to the instance.
func (s *Service) DumpSpec(ctx context.Context, inst *models.DatabaseInstance, db *models.Database, name string) (docker.RunSpec, error) {
	return s.streamSpec(ctx, inst, db, name, true)
}

// RestoreSpec is a one-shot container that loads a dump from stdin into a logical database. The database
// must exist and be empty: CreateDatabase or RecreateDatabase first.
func (s *Service) RestoreSpec(ctx context.Context, inst *models.DatabaseInstance, db *models.Database, name string) (docker.RunSpec, error) {
	return s.streamSpec(ctx, inst, db, name, false)
}

func (s *Service) streamSpec(ctx context.Context, inst *models.DatabaseInstance, db *models.Database, name string, dump bool) (docker.RunSpec, error) {
	if !inst.SupportsLogicalDatabases() || inst.Engine == models.DBEngineLibSQL {
		return docker.RunSpec{}, ErrUnsupportedEngine
	}
	adminPass, err := crypto.Decrypt(inst.AdminPasswordEnc)
	if err != nil {
		return docker.RunSpec{}, err
	}
	dc, err := s.dockerFor(inst)
	if err != nil {
		return docker.RunSpec{}, err
	}
	nets, err := s.ensureInstanceNetworks(ctx, dc, inst)
	if err != nil {
		return docker.RunSpec{}, err
	}
	cmd, env := streamInvocation(inst, db, adminPass, dump)
	return docker.RunSpec{
		Name:     name,
		Image:    s.engineImage(specs[inst.Engine], inst),
		Cmd:      cmd,
		Env:      env,
		Networks: nets,
	}, nil
}

// streamInvocation is the engine's own dump or restore client, run as the instance admin against the
// instance by its alias. The restored objects end up owned by the database's scoped user.
func streamInvocation(inst *models.DatabaseInstance, db *models.Database, adminPass string, dump bool) (cmd, env []string) {
	port := strconv.Itoa(inst.Port)
	switch inst.Engine {
	case models.DBEnginePostgres:
		env = []string{"PGPASSWORD=" + adminPass}
		conn := []string{"-h", inst.Host, "-p", port, "-U", inst.AdminUser, "-d", db.Name}
		if dump {
			return append(append([]string{"pg_dump"}, conn...), "-Fc", "--no-owner", "--no-acl"), env
		}
		return append(append([]string{"pg_restore"}, conn...),
			"--no-owner", "--no-acl", "--role="+db.Username, "--single-transaction", "--exit-on-error"), env
	case models.DBEngineMongoDB:
		conn := []string{"--host", inst.Host, "--port", port, "--username", inst.AdminUser,
			"--password", adminPass, "--authenticationDatabase", "admin", "--archive"}
		if dump {
			return append(append([]string{"mongodump"}, conn...), "--db", db.Name), nil
		}
		return append(append([]string{"mongorestore"}, conn...), "--nsInclude", db.Name+".*", "--drop"), nil
	default:
		dumper, client := "mysqldump", "mysql"
		if inst.Engine == models.DBEngineMariaDB {
			dumper, client = "mariadb-dump", "mariadb"
		}
		env = []string{"MYSQL_PWD=" + adminPass}
		conn := []string{"-h", inst.Host, "-P", port, "-u", inst.AdminUser}
		if dump {
			return append(append([]string{dumper}, conn...),
				"--single-transaction", "--routines", "--triggers", "--events", "--no-tablespaces", db.Name), env
		}
		return append(append([]string{client}, conn...), db.Name), env
	}
}

// ErrTargetOlder refuses restoring into an instance older than the source: a dump from a newer engine may
// use what the older one cannot read.
var ErrTargetOlder = errors.New("the target instance runs an older version than the source")

// CanRestoreInto reports whether a logical dump of src loads into dst: the same engine, at the same
// version or newer.
func CanRestoreInto(src, dst *models.DatabaseInstance) error {
	if src.Engine != dst.Engine {
		return fmt.Errorf("the target instance runs %s, not %s", dst.Engine, src.Engine)
	}
	if cmpVersion(src.Version, dst.Version) > 0 {
		return ErrTargetOlder
	}
	return nil
}
