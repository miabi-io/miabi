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

package alerting

import (
	"fmt"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/backup"
	"github.com/miabi-io/miabi/internal/services/restores"
)

// BackupReporter posts finished backup runs to the inbox.
type BackupReporter struct{ n *WorkspaceNotifier }

func NewBackupReporter(n *WorkspaceNotifier) BackupReporter { return BackupReporter{n: n} }

func (r BackupReporter) ScheduledBackupFinished(ws uint, rep backup.BackupReport) {
	// Developer and up, matching who receives the backup_failed alert: a viewer cannot act on either.
	if err := r.n.NotifyWorkspace(ws, models.WorkspaceRoleDeveloper, backupNotice(rep, "The scheduled backup")); err != nil {
		logger.Warn("could not report a scheduled backup to the workspace inbox", "workspace", ws, "error", err)
	}
}

func (r BackupReporter) RequestedBackupFinished(ws, user uint, rep backup.BackupReport) {
	if err := r.n.NotifyUser(ws, user, backupNotice(rep, "The backup")); err != nil {
		logger.Warn("could not report a backup to its requester", "workspace", ws, "user", user, "error", err)
	}
}

func backupNotice(rep backup.BackupReport, run string) models.Notification {
	item := models.Notification{
		Category:    rep.Category,
		SubjectLink: rep.Link,
		ActionText:  "View backups",
	}
	if item.Category == "" {
		item.Category = models.CategoryDatabase
	}
	what := "Backup"
	if rep.Ref != "" {
		what = "Recovery point"
	}
	switch {
	case rep.OK && rep.Databases > 0:
		item.Severity = models.AlertInfo
		item.Title = fmt.Sprintf("%s completed — %s", what, rep.Subject)
		item.Body = fmt.Sprintf("%s covered %s and stored %s.",
			rep.Ref, pluralDatabases(rep.Databases), humanBytes(rep.SizeBytes))
	case rep.OK:
		item.Severity = models.AlertInfo
		item.Title = fmt.Sprintf("%s completed — %s", what, rep.Subject)
		item.Body = fmt.Sprintf("%s stored %s.", run, humanBytes(rep.SizeBytes))
	default:
		item.Severity = models.AlertWarning
		item.Title = fmt.Sprintf("%s failed — %s", what, rep.Subject)
		item.Body = rep.Err
		if rep.Ref != "" {
			item.Body = rep.Ref + ": " + rep.Err
		}
	}
	return item
}

func pluralDatabases(n int) string {
	if n == 1 {
		return "1 database"
	}
	return fmt.Sprintf("%d databases", n)
}

// Deliberately coarse: the exact byte count belongs on the backup row, not in a sentence.
func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	val, exp := float64(b), 0
	for val >= unit && exp < 4 {
		val /= unit
		exp++
	}
	return fmt.Sprintf("%.1f %sB", val, [...]string{"", "Ki", "Mi", "Gi", "Ti"}[exp])
}

func (r BackupReporter) RequestedRestoreFinished(ws, user uint, rep restores.Report) {
	if err := r.n.NotifyUser(ws, user, restoreNotice(rep)); err != nil {
		logger.Warn("could not report a restore to its requester", "workspace", ws, "user", user, "error", err)
	}
}

func restoreNotice(rep restores.Report) models.Notification {
	item := models.Notification{
		Category: models.CategoryDatabase, SubjectLink: rep.Link, ActionText: "View",
		Severity: models.AlertInfo,
	}
	if rep.Kind == models.RestoreVolume {
		item.Category = models.CategoryStorage
	}
	switch {
	case rep.OK && rep.Kind == models.RestoreRecoveryPoint:
		item.Title = fmt.Sprintf("Recovery point restored — %s", rep.Subject)
		item.Body = fmt.Sprintf("%s restored %s.", rep.Source, pluralDatabases(len(rep.Restored)))
	case rep.OK:
		item.Title = fmt.Sprintf("Restore completed — %s", rep.Subject)
		item.Body = fmt.Sprintf("Restored from %s.", rep.Source)
	case len(rep.Restored) > 0:
		item.Severity = models.AlertWarning
		item.Title = fmt.Sprintf("Restore partly failed — %s", rep.Subject)
		item.Body = fmt.Sprintf("%s restored %s; failed: %s", rep.Source, pluralDatabases(len(rep.Restored)), strings.Join(rep.Failed, "; "))
	default:
		item.Severity = models.AlertWarning
		item.Title = fmt.Sprintf("Restore failed — %s", rep.Subject)
		item.Body = rep.Err
		if len(rep.Failed) > 0 {
			item.Body = strings.Join(rep.Failed, "; ")
		}
	}
	return item
}
