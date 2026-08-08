// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package dbupgrade adapts the application and backup services to the small interfaces the database
// version-upgrade orchestration depends on, so both the API server and the worker can wire them without
// the database package importing application or backup, which would cycle.
package dbupgrade

import (
	"context"
	"fmt"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/application"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/database"
)

// AppController adapts the application service to database.AppController.
func AppController(apps *application.Service) database.AppController { return appController{apps} }

type appController struct{ apps *application.Service }

func (a appController) StopByID(ctx context.Context, workspaceID, appID uint) error {
	app, err := a.apps.Get(workspaceID, appID)
	if err != nil {
		return err
	}
	return a.apps.Stop(ctx, app)
}

func (a appController) StartByID(ctx context.Context, workspaceID, appID uint) error {
	app, err := a.apps.Get(workspaceID, appID)
	if err != nil {
		return err
	}
	_, err = a.apps.Start(ctx, app)
	return err
}

// Backup adapts the backup service to database.LogicalBackup.
func Backup(b *backup.Service) database.LogicalBackup { return logicalBackup{b} }

type logicalBackup struct{ b *backup.Service }

func (d logicalBackup) Dump(ctx context.Context, inst *models.DatabaseInstance, db *models.Database) (database.DumpRef, error) {
	bk, err := d.b.Run(ctx, inst, db, "upgrade", backup.Destination{Type: "local"})
	if err != nil {
		return database.DumpRef{}, err
	}
	if bk.Status != models.BackupCompleted {
		return database.DumpRef{}, fmt.Errorf("backup did not complete: %s", bk.Error)
	}
	return database.DumpRef{Filename: bk.Filename, Volume: bk.VolumeName}, nil
}

func (d logicalBackup) Load(ctx context.Context, inst *models.DatabaseInstance, db *models.Database, ref database.DumpRef, force bool) error {
	return d.b.Restore(ctx, inst, db, backup.RestoreSpec{
		Filename:    ref.Filename,
		Destination: "local",
		VolumeName:  ref.Volume,
		Force:       force,
	})
}
