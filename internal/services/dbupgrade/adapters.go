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

// Package dbupgrade adapts the app and backup services to the database upgrade interfaces without an import cycle.
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

func (d logicalBackup) Dump(ctx context.Context, inst *models.DatabaseInstance, db *models.Database, comment string) (database.DumpRef, error) {
	bk, err := d.b.Run(ctx, inst, db, backup.RunOptions{Trigger: "upgrade", Comment: comment}, backup.Destination{Type: "local"})
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
