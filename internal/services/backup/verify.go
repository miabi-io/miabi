// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/dbenvelope"
	"github.com/miabi-io/miabi/internal/models"
)

// Verification outcomes recorded on a set.
const (
	VerifyOK     = "ok"
	VerifyFailed = "failed"
)

// VerifyResult is what a check found. It names artifacts rather than only counting
// them: "one file is missing" is not actionable, "billing's dump is gone" is.
type VerifyResult struct {
	Ref     string `json:"ref"`
	OK      bool   `json:"ok"`
	Checked int    `json:"checked"`
	// Missing are artifacts the set recorded that are no longer in the bucket —
	// usually a lifecycle rule, occasionally an upload nobody noticed had failed.
	Missing []string `json:"missing,omitempty"`
	// Resized are artifacts whose stored size no longer matches what was recorded,
	// which means the object is not the one this set took.
	Resized []string `json:"resized,omitempty"`
	// EnvelopeOK reports that the workspace passphrase still opens the data key. A
	// set can be perfectly intact in the bucket and unreadable all the same.
	EnvelopeOK bool   `json:"envelope_ok"`
	Error      string `json:"error,omitempty"`
}

// VerifySet checks a recovery point against the bucket: every artifact is still
// there, still the size it was, and the envelope still opens.
//
// It deliberately does not read the dumps back. That costs the size of the backup
// every time, which is how verification becomes the thing people switch off.
// Proving a dump actually loads is a drill, and a drill needs a live engine.
func (s *Service) VerifySet(ctx context.Context, cfg *S3Config, set *models.DatabaseBackupSet, passphrase string) (*VerifyResult, error) {
	if s.sets == nil {
		return nil, ErrSetsUnavailable
	}
	if set == nil {
		return nil, fmt.Errorf("no recovery point")
	}
	store, err := blobStoreFor(cfg)
	if err != nil {
		return nil, err
	}
	objects, err := store.List(ctx, strings.Trim(set.S3Path, "/"))
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", set.S3Path, err)
	}
	sizes := make(map[string]int64, len(objects))
	for _, o := range objects {
		sizes[path.Base(o.Key)] = o.Size
	}

	res := checkSetAgainstBucket(set, sizes, passphrase)
	s.recordVerification(set, res)
	return res, nil
}

// checkSetAgainstBucket is the judgement, separated from the bucket read so it is
// testable without one.
func checkSetAgainstBucket(set *models.DatabaseBackupSet, sizes map[string]int64, passphrase string) *VerifyResult {
	res := &VerifyResult{Ref: set.Ref}
	for i := range set.Items {
		it := &set.Items[i]
		if it.Filename == "" {
			continue
		}
		res.Checked++
		size, ok := sizes[it.Filename]
		switch {
		case !ok:
			res.Missing = append(res.Missing, it.Filename)
		case it.SizeBytes > 0 && size != it.SizeBytes:
			res.Resized = append(res.Resized, it.Filename)
		}
	}
	sort.Strings(res.Missing)
	sort.Strings(res.Resized)

	res.EnvelopeOK = set.Envelope == ""
	envelopeErr := ""
	if set.Envelope != "" {
		if passphrase == "" {
			envelopeErr = "encrypted, and no workspace backup passphrase is set to check it with"
		} else if _, err := dbenvelope.Open(set.Envelope, passphrase); err != nil {
			envelopeErr = "the workspace passphrase no longer opens this recovery point"
		} else {
			res.EnvelopeOK = true
		}
	}

	switch {
	case res.Checked == 0:
		res.Error = "the recovery point has no artifacts to check"
	case len(res.Missing) > 0:
		res.Error = fmt.Sprintf("%d of %d artifacts are missing from the bucket: %s",
			len(res.Missing), res.Checked, strings.Join(res.Missing, ", "))
	case len(res.Resized) > 0:
		res.Error = fmt.Sprintf("%d artifact(s) are not the size they were stored at: %s",
			len(res.Resized), strings.Join(res.Resized, ", "))
	default:
		res.Error = envelopeErr
	}
	res.OK = res.Error == "" && res.EnvelopeOK
	return res
}

// recordVerification persists the outcome and raises a failure where someone will
// see it. A check that fails silently is worth nothing.
func (s *Service) recordVerification(set *models.DatabaseBackupSet, res *VerifyResult) {
	now := time.Now()
	set.VerifiedAt = &now
	set.VerifyError = res.Error
	set.VerifyStatus = VerifyFailed
	if res.OK {
		set.VerifyStatus = VerifyOK
	}
	if err := s.sets.Update(set); err != nil {
		logger.Error("record recovery point verification", "set", set.Ref, "error", err)
	}
	if res.OK {
		return
	}
	logger.Warn("recovery point failed verification", "set", set.Ref, "error", res.Error)
	if s.alerter != nil {
		// Raised against the instance, not a logical database: a set spans all of
		// them and no single one is at fault.
		s.alerter.BackupFailed(set.WorkspaceID, set.InstanceID, set.Ref, "verification: "+res.Error)
	}
	s.emit(set.WorkspaceID, set.InstanceID, set.Ref, models.EventDatabaseBackupFailed, models.SeverityWarning,
		fmt.Sprintf("Recovery point %s failed verification: %s", set.Ref, res.Error),
		map[string]string{"set": set.Ref, "check": "verify"})
}
