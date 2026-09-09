// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/dbenvelope"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	// ErrSetsUnavailable means the set repository was never wired.
	ErrSetsUnavailable = errors.New("backup sets are not available")
	// ErrNoDatabases means the instance has no logical database to back up. Redis
	// reaches here too: its data rides on the volume, so it has no logical
	// databases and is covered by a volume backup instead.
	ErrNoDatabases = errors.New("the instance has no database to back up")
	// ErrS3Required means the workspace has no object-storage target. A recovery
	// point exists to survive the loss of the host it was taken from, and the local
	// backup volume dies with that host — so a local set would promise something it
	// cannot deliver. Rediscovering sets from a bucket depends on this too.
	ErrS3Required = errors.New("backup sets require the workspace S3 backup target")
)

// defaultSetConcurrency is deliberately 1. Several dumps at once against one
// engine container is felt by whatever else is using it, and a backup that slows
// production is a backup people turn off.
const defaultSetConcurrency = 1

// SetOptions tunes a set run.
type SetOptions struct {
	Trigger string // manual | scheduled
	Comment string
	// Concurrency caps how many databases are dumped at once. Zero uses
	// defaultSetConcurrency.
	Concurrency int
}

// SetSetRepository wires recovery-point storage (nil-safe: unset means RunSet and
// the prune below report ErrSetsUnavailable rather than panicking).
func (s *Service) SetSetRepository(r *repositories.DatabaseBackupSetRepository) { s.sets = r }

// RunSet backs up every logical database on an instance as one recovery point.
// It requires the workspace S3 target and returns ErrS3Required without one.
//
// The set is marked failed if any item fails, and the error names the first
// failure: a recovery point that is missing a database is not one, and reporting
// it as a success is how people discover the gap during an incident instead of
// after the run.
func (s *Service) RunSet(ctx context.Context, inst *models.DatabaseInstance, opts SetOptions, dest Destination) (*models.DatabaseBackupSet, error) {
	if s.sets == nil {
		return nil, ErrSetsUnavailable
	}
	if _, ok := s.bkupImage(inst.Engine); !ok {
		return nil, ErrUnsupportedEngine
	}
	// Cheapest precondition first: refuse before touching the database or the node.
	if dest.Type != "s3" || dest.S3 == nil {
		return nil, ErrS3Required
	}
	dbs, err := s.dbs.ListDatabases(inst.ID)
	if err != nil {
		return nil, err
	}
	if len(dbs) == 0 {
		return nil, ErrNoDatabases
	}

	var envelope string
	// dest.GPGPassphrase becomes the data key below, so the passphrase that opens
	// the envelope has to be kept for the verification that follows the run.
	envelopePassphrase := dest.GPGPassphrase

	// The artifacts are encrypted with a per-set data key rather than the passphrase
	// itself; the envelope is what ties that key back to the passphrase. Without a
	// passphrase the set is unencrypted, exactly as a per-database backup would be.
	if dest.GPGPassphrase != "" {
		dataKey, kerr := dbenvelope.NewDataKey()
		if kerr != nil {
			return nil, kerr
		}
		sealed, serr := dbenvelope.Seal(dataKey, dest.GPGPassphrase)
		if serr != nil {
			return nil, fmt.Errorf("seal the set data key: %w", serr)
		}
		envelope = sealed
		dest.GPGPassphrase = dataKey
	}

	now := time.Now()
	ref := models.NewDatabaseBackupSetRef(inst.Name, now)
	// One prefix per set, so discovery can read a recovery point as a unit instead
	// of parsing filenames out of a prefix every set shares.
	dest.S3.Path = SetPrefix(dest.S3.Path, inst.Name, ref)

	set := &models.DatabaseBackupSet{
		WorkspaceID: inst.WorkspaceID,
		InstanceID:  inst.ID,
		Ref:         ref,
		Trigger:     opts.Trigger,
		Status:      models.BackupRunning,
		Engine:      inst.Engine,
		Version:     inst.Version,
		Destination: dest.Type,
		S3Bucket:    dest.S3.Bucket,
		S3Path:      dest.S3.Path,
		Envelope:    envelope,
		StartedAt:   &now,
	}
	if err := s.sets.Create(set); err != nil {
		return nil, err
	}

	items := s.runSetItems(ctx, inst, dbs, set.ID, opts, dest)
	done := s.finishSet(set, inst, items)

	// The descriptor is what makes the set readable from the bucket alone. Written
	// last, so it describes what actually landed; best-effort, because failing here
	// would discard a recovery point whose dumps are already safely stored.
	names := make(map[uint]string, len(dbs))
	for i := range dbs {
		names[dbs[i].ID] = dbs[i].Name
	}
	done.Items = derefItems(items)
	if err := writeSetInfo(ctx, dest.S3, dest.S3.Path, setInfoFor(done, inst.Name, names)); err != nil {
		logger.Error("write recovery point descriptor; the set is stored but will not be discoverable",
			"set", done.Ref, "error", err)
	}

	// Check it immediately. A backup nobody has read back is a backup nobody knows
	// works, and the cheapest moment to find a failed upload is right after it.
	if done.Status == models.BackupCompleted {
		if _, err := s.VerifySet(ctx, dest.S3, done, envelopePassphrase); err != nil {
			logger.Warn("could not verify the new recovery point", "set", done.Ref, "error", err)
		}
	}
	return done, nil
}

// derefItems collects the non-nil item records, so the descriptor and the returned
// set carry what the run actually produced.
func derefItems(items []*models.Backup) []models.Backup {
	out := make([]models.Backup, 0, len(items))
	for _, b := range items {
		if b != nil {
			out = append(out, *b)
		}
	}
	return out
}

// runSetItems dumps each database, at most Concurrency at a time, and returns the
// items in the order the databases were listed.
func (s *Service) runSetItems(ctx context.Context, inst *models.DatabaseInstance, dbs []models.Database,
	setID uint, opts SetOptions, dest Destination) []*models.Backup {

	limit := opts.Concurrency
	if limit <= 0 {
		limit = defaultSetConcurrency
	}
	if limit > len(dbs) {
		limit = len(dbs)
	}

	items := make([]*models.Backup, len(dbs))
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := range dbs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			db := dbs[i]
			b, err := s.Run(ctx, inst, &db, RunOptions{
				Trigger: opts.Trigger, Comment: opts.Comment, SetID: &setID,
			}, dest)
			if err != nil {
				// Run returns an error only when it could not start; a dump that ran
				// and failed comes back as a failed record. Synthesize one so the set
				// still names the database that stopped it.
				logger.Error("backup set item could not start", "set", setID, "database", db.Name, "error", err)
				items[i] = &models.Backup{DatabaseID: db.ID, Status: models.BackupFailed, Error: err.Error()}
				return
			}
			items[i] = b
		}(i)
	}
	wg.Wait()
	return items
}

// finishSet aggregates the items into the set's outcome and persists it.
func (s *Service) finishSet(set *models.DatabaseBackupSet, inst *models.DatabaseInstance, items []*models.Backup) *models.DatabaseBackupSet {
	fin := time.Now()
	set.FinishedAt = &fin
	set.Status = models.BackupCompleted
	encrypted := len(items) > 0

	for _, b := range items {
		if b == nil {
			continue
		}
		if b.Status != models.BackupCompleted {
			set.Status = models.BackupFailed
			if set.Error == "" {
				set.Error = fmt.Sprintf("database %d: %s", b.DatabaseID, b.Error)
			}
			continue
		}
		set.SizeBytes += b.SizeBytes
		if !b.Encrypted {
			encrypted = false
		}
	}
	// Only a set whose every artifact is sealed can be described as encrypted; a
	// mixed set would need the passphrase for some items and not others.
	set.Encrypted = encrypted && set.Status == models.BackupCompleted

	if err := s.sets.Update(set); err != nil {
		logger.Error("finalize backup set", "set", set.ID, "error", err)
	}
	sev, msg := models.SeverityInfo, fmt.Sprintf("Backup set %s completed (%d databases)", set.Ref, len(items))
	evt := models.EventDatabaseBackupSucceeded
	if set.Status == models.BackupFailed {
		sev, evt = models.SeverityError, models.EventDatabaseBackupFailed
		msg = fmt.Sprintf("Backup set %s failed: %s", set.Ref, set.Error)
	}
	s.emit(set.WorkspaceID, inst.ID, inst.Name, evt, sev, msg,
		map[string]string{"set": set.Ref, "databases": fmt.Sprint(len(items))})
	return set
}

// ListSets returns an instance's recovery points, newest first.
func (s *Service) ListSets(instanceID uint) ([]models.DatabaseBackupSet, error) {
	if s.sets == nil {
		return nil, ErrSetsUnavailable
	}
	return s.sets.ListByInstance(instanceID)
}

// GetSet loads one recovery point with its items.
func (s *Service) GetSet(workspaceID, id uint) (*models.DatabaseBackupSet, error) {
	if s.sets == nil {
		return nil, ErrSetsUnavailable
	}
	return s.sets.FindInWorkspace(workspaceID, id)
}

// DeleteSet removes a recovery point and every artifact it carries. The items'
// rows cascade with the set, but their artifacts are objects in a bucket, which
// only this service can remove.
func (s *Service) DeleteSet(ctx context.Context, set *models.DatabaseBackupSet) error {
	if s.sets == nil {
		return ErrSetsUnavailable
	}
	for i := range set.Items {
		if err := s.Delete(ctx, &set.Items[i]); err != nil {
			return fmt.Errorf("remove backup %d: %w", set.Items[i].ID, err)
		}
	}
	return s.sets.Delete(set.ID)
}

// PruneSets enforces retention over an instance's recovery points: keep at most
// maxSets most-recent, and delete any older than retentionDays. A zero bound is
// ignored. Returns the number of sets removed.
//
// Three rules the per-database prune does not have:
//
//   - Whole sets only. Pruning items out of a set would leave the half recovery
//     point the set exists to prevent.
//   - A set with any pinned member is never pruned, and does not occupy a
//     maxSets slot, mirroring how a pinned backup behaves.
//   - The newest completed set always survives, whatever the policy says. A
//     mistyped retention must not be able to leave an instance with nothing.
func (s *Service) PruneSets(ctx context.Context, instanceID uint, maxSets, retentionDays int) (int, error) {
	if s.sets == nil {
		return 0, ErrSetsUnavailable
	}
	if maxSets <= 0 && retentionDays <= 0 {
		return 0, nil
	}
	sets, err := s.sets.ListByInstance(instanceID) // newest-first
	if err != nil {
		return 0, err
	}

	keep := uint(0)
	for i := range sets {
		if sets[i].Status == models.BackupCompleted {
			keep = sets[i].ID
			break
		}
	}
	var cutoff time.Time
	if retentionDays > 0 {
		cutoff = time.Now().AddDate(0, 0, -retentionDays)
	}

	removed, rank := 0, 0
	for i := range sets {
		set := &sets[i]
		if set.HasPinnedItem() {
			continue
		}
		overCount := maxSets > 0 && rank >= maxSets
		tooOld := retentionDays > 0 && set.CreatedAt.Before(cutoff)
		rank++
		if set.ID == keep || !(overCount || tooOld) {
			continue
		}
		if err := s.DeleteSet(ctx, set); err != nil {
			logger.Error("prune backup set", "set", set.ID, "error", err)
			continue
		}
		removed++
	}
	if removed > 0 {
		logger.Info("pruned backup sets", "instance", instanceID, "removed", removed)
	}
	return removed, nil
}

// RewrapSets re-seals every sealed recovery point in a workspace from one
// passphrase to the next. This is the whole reason the artifacts are encrypted
// with a per-set data key: rotation rewrites a few hundred bytes per set and never
// re-reads a dump.
//
// It stops at the first envelope that will not open, because that means the old
// passphrase is wrong and continuing would rewrap the rest under a mismatched
// secret. Sets already rewrapped stay rewrapped; the operation is resumable.
func (s *Service) RewrapSets(workspaceID uint, oldPassphrase, newPassphrase string) (int, error) {
	if s.sets == nil {
		return 0, ErrSetsUnavailable
	}
	if oldPassphrase == "" || newPassphrase == "" || oldPassphrase == newPassphrase {
		return 0, nil
	}
	sealed, err := s.sets.ListSealed(workspaceID)
	if err != nil {
		return 0, err
	}
	rewrapped := 0
	for i := range sealed {
		set := &sealed[i]
		next, err := dbenvelope.Rewrap(set.Envelope, oldPassphrase, newPassphrase)
		if err != nil {
			return rewrapped, fmt.Errorf("recovery point %s: %w", set.Ref, err)
		}
		set.Envelope = next
		if err := s.sets.Update(set); err != nil {
			return rewrapped, fmt.Errorf("recovery point %s: %w", set.Ref, err)
		}
		rewrapped++
	}
	if rewrapped > 0 {
		logger.Info("rewrapped backup set envelopes", "workspace", workspaceID, "sets", rewrapped)
	}
	return rewrapped, nil
}

// SealedSetCount reports how many recovery points in a workspace are sealed, so a
// caller can refuse to discard the passphrase that opens them.
func (s *Service) SealedSetCount(workspaceID uint) (int, error) {
	if s.sets == nil {
		return 0, nil
	}
	sealed, err := s.sets.ListSealed(workspaceID)
	return len(sealed), err
}

// SetRestoreResult reports what a set-level restore did.
type SetRestoreResult struct {
	Ref      string   `json:"ref"`
	Restored []string `json:"restored"`
	Failed   []string `json:"failed,omitempty"`
}

// RestoreSet restores every database in a recovery point, in one operation.
//
// Unlike a backup, this does NOT stop at the first failure. A restore is run when
// something has already gone wrong, and abandoning the remaining databases because
// one of them failed would turn a partial recovery into a smaller one. Every
// outcome is reported and the caller decides.
func (s *Service) RestoreSet(ctx context.Context, inst *models.DatabaseInstance, set *models.DatabaseBackupSet,
	dest Destination, force, allowVersionMismatch bool) (*SetRestoreResult, error) {

	if s.sets == nil {
		return nil, ErrSetsUnavailable
	}
	if len(set.Items) == 0 {
		return nil, ErrNoBackupFile
	}
	res := &SetRestoreResult{Ref: set.Ref}
	for i := range set.Items {
		item := &set.Items[i]
		db, err := s.dbs.FindDatabaseInWorkspace(set.WorkspaceID, item.DatabaseID)
		if err != nil {
			res.Failed = append(res.Failed, fmt.Sprintf("database #%d: %v", item.DatabaseID, err))
			continue
		}
		if err := s.RestoreFromBackup(ctx, inst, db, item, dest, force, allowVersionMismatch); err != nil {
			res.Failed = append(res.Failed, fmt.Sprintf("%s: %v", db.Name, err))
			continue
		}
		res.Restored = append(res.Restored, db.Name)
	}
	sev, msg := models.SeverityInfo, fmt.Sprintf("Restored %d database(s) from %s", len(res.Restored), set.Ref)
	evt := models.EventDatabaseRestoreSucceeded
	if len(res.Failed) > 0 {
		sev, evt = models.SeverityError, models.EventDatabaseRestoreFailed
		msg = fmt.Sprintf("Restore of %s: %d succeeded, %d failed", set.Ref, len(res.Restored), len(res.Failed))
	}
	s.emit(set.WorkspaceID, inst.ID, inst.Name, evt, sev, msg,
		map[string]string{"set": set.Ref, "restored": fmt.Sprint(len(res.Restored)), "failed": fmt.Sprint(len(res.Failed))})
	return res, nil
}
