// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package logbackfill migrates pre-existing large log rows out of Postgres into
// the shared log store
// It runs at boot rather than as a once-only migration.Step because it needs the
// log store (which a Step's (ctx, db)-only signature can't provide) and must be
// deferred until the store is actually enabled: when MIABI_LOG_BACKEND=off it is
// a no-op that records nothing, so enabling the store on a later boot still
// triggers the backfill. Once it completes with the store enabled it records an
// UpgradeStep marker and never scans again.
package logbackfill

import (
	"context"
	"fmt"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/logstore"
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// stepName marks the backfill complete in the upgrade_steps table (reusing the
// same once-only ledger the migration/upgrade package uses).
const stepName = "2026.07-log-externalize-backfill"

// batchSize bounds how many rows are loaded (with their full logs) at once, so a
// large history doesn't pull every log into memory in one query.
const batchSize = 200

// Run externalizes every log-bearing row whose stored log exceeds thresholdBytes
// and has not yet been externalized. No-op (and unrecorded) when the store is
// disabled, so a later boot with the store enabled still runs it. thresholdBytes
// is the DB tail size: a log already no larger than a tail isn't worth moving —
// the reader serves it from the tail with an empty ref.
func Run(ctx context.Context, db *gorm.DB, store *logstore.Store, thresholdBytes int, version string) error {
	if !store.Enabled() {
		return nil
	}
	var applied int64
	if err := db.WithContext(ctx).Model(&models.UpgradeStep{}).
		Where("name = ?", stepName).Count(&applied).Error; err != nil {
		return fmt.Errorf("logbackfill: check marker: %w", err)
	}
	if applied > 0 {
		return nil
	}
	if thresholdBytes <= 0 {
		thresholdBytes = 16 << 10
	}

	total := 0
	for _, kind := range kinds() {
		n, err := backfillKind(ctx, db, store, kind, thresholdBytes)
		if err != nil {
			// Don't record the marker: the next boot retries the remainder.
			return fmt.Errorf("logbackfill: %s: %w", kind.name, err)
		}
		total += n
	}
	if total > 0 {
		logger.Info("log store: backfilled existing logs into the store", "rows", total)
	}

	rec := &models.UpgradeStep{Name: stepName, Version: version, AppliedAt: time.Now()}
	if err := db.WithContext(ctx).Create(rec).Error; err != nil {
		return fmt.Errorf("logbackfill: record marker: %w", err)
	}
	return nil
}

// logRow is one externalizable row: its own id, the full log, and the identifiers
// its object key is derived from (workspace/parent/ordinal, per kind).
type logRow struct {
	ID          uint
	WorkspaceID uint
	ParentID    uint // app id (deployment) or run id (pipeline step); unused otherwise
	Ordinal     int  // pipeline step ordinal; unused otherwise
	Logs        string
}

// kind describes one log-bearing table: how to select its externalizable rows
// (joining for the workspace id where the row doesn't carry it) and how to build
// each row's object key.
type kind struct {
	name  string
	table string
	// query returns rows with COALESCE(log_ref,'')='' and length(logs) > threshold,
	// limited to batchSize, each populated with the fields refFor needs.
	query  func(ctx context.Context, db *gorm.DB, threshold int) ([]logRow, error)
	refFor func(r logRow) string
}

func kinds() []kind {
	return []kind{
		{
			name:  "deployment",
			table: "deployments",
			query: joinQuery(
				"deployments d", "JOIN applications a ON a.id = d.application_id",
				"d.id AS id, a.workspace_id AS workspace_id, d.application_id AS parent_id, d.logs AS logs", "d"),
			refFor: func(r logRow) string { return logstore.DeploymentRef(r.WorkspaceID, r.ParentID, r.ID) },
		},
		{
			name:  "pipeline-step",
			table: "pipeline_step_runs",
			query: joinQuery(
				"pipeline_step_runs s", "JOIN pipeline_runs r ON r.id = s.pipeline_run_id",
				"s.id AS id, r.workspace_id AS workspace_id, s.pipeline_run_id AS parent_id, s.ordinal AS ordinal, s.logs AS logs", "s"),
			refFor: func(r logRow) string { return logstore.PipelineStepRef(r.WorkspaceID, r.ParentID, r.Ordinal) },
		},
		{
			name:   "job",
			table:  "jobs",
			query:  plainQuery("jobs", "id, workspace_id, logs"),
			refFor: func(r logRow) string { return logstore.JobRef(r.WorkspaceID, r.ID) },
		},
		{
			name:   "backup",
			table:  "backups",
			query:  plainQuery("backups", "id, workspace_id, logs"),
			refFor: func(r logRow) string { return logstore.BackupRef(r.WorkspaceID, r.ID) },
		},
		{
			name:   "volume-backup",
			table:  "volume_backups",
			query:  plainQuery("volume_backups", "id, workspace_id, logs"),
			refFor: func(r logRow) string { return logstore.VolumeBackupRef(r.WorkspaceID, r.ID) },
		},
		{
			name:   "platform-backup",
			table:  "platform_backups",
			query:  plainQuery("platform_backups", "id, logs"),
			refFor: func(r logRow) string { return logstore.PlatformBackupRef(r.ID) },
		},
	}
}

// plainQuery selects externalizable rows from a table that carries every field
// its ref needs on the row itself.
func plainQuery(table, cols string) func(context.Context, *gorm.DB, int) ([]logRow, error) {
	return func(ctx context.Context, db *gorm.DB, threshold int) ([]logRow, error) {
		var rows []logRow
		err := db.WithContext(ctx).Table(table).Select(cols).
			Where("COALESCE(log_ref, '') = '' AND length(logs) > ?", threshold).
			Order("id").Limit(batchSize).Scan(&rows).Error
		return rows, err
	}
}

// joinQuery selects externalizable rows from a table that must join a parent for
// the workspace id (deployments → applications, pipeline steps → runs).
func joinQuery(from, join, cols, alias string) func(context.Context, *gorm.DB, int) ([]logRow, error) {
	where := fmt.Sprintf("COALESCE(%s.log_ref, '') = '' AND length(%s.logs) > ?", alias, alias)
	order := alias + ".id"
	return func(ctx context.Context, db *gorm.DB, threshold int) ([]logRow, error) {
		var rows []logRow
		err := db.WithContext(ctx).Table(from).Joins(join).Select(cols).
			Where(where, threshold).Order(order).Limit(batchSize).Scan(&rows).Error
		return rows, err
	}
}

// backfillKind drains one table's externalizable rows in batches, writing each
// full log to the store and trimming the row to a tail + ref.
func backfillKind(ctx context.Context, db *gorm.DB, store *logstore.Store, k kind, threshold int) (int, error) {
	done := 0
	for {
		rows, err := k.query(ctx, db, threshold)
		if err != nil {
			return done, fmt.Errorf("load rows: %w", err)
		}
		if len(rows) == 0 {
			return done, nil
		}
		for _, r := range rows {
			ref := k.refFor(r)
			res, err := store.Externalize(ref, r.Logs)
			if err != nil {
				return done, fmt.Errorf("externalize id=%d: %w", r.ID, err)
			}
			if res.Ref == "" {
				// Store disabled mid-run (shouldn't happen; Run guards it). Stop
				// so we don't loop forever on rows that never gain a ref.
				return done, nil
			}
			if err := db.WithContext(ctx).Table(k.table).Where("id = ?", r.ID).
				Updates(map[string]any{
					"logs":          res.Tail,
					"log_ref":       res.Ref,
					"log_bytes":     res.Bytes,
					"log_lines":     res.Lines,
					"log_truncated": res.Truncated,
				}).Error; err != nil {
				return done, fmt.Errorf("update id=%d: %w", r.ID, err)
			}
			done++
		}
		// A short batch means the table is drained.
		if len(rows) < batchSize {
			return done, nil
		}
	}
}
