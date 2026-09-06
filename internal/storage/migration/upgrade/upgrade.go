// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package upgrade runs ordered, versioned data-upgrade steps. Each step runs at
// most once (tracked by models.UpgradeStep). Schema DDL belongs in the
// migration package; this is for data backfills and one-off transformations.
package upgrade

import (
	"context"
	"fmt"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/gorm"
)

// Options controls upgrade behavior.
type Options struct {
	// AllowDowngrade permits booting on a binary older than the recorded version.
	AllowDowngrade bool
}

// Step is a single, idempotent upgrade unit.
type Step struct {
	Name    string
	Version string
	Run     func(ctx context.Context, db *gorm.DB) error
}

// steps is the ordered registry of upgrade steps, filled by each step file's init. Go runs
// those in filename order, so the steps_YYYY_MM_DD_ prefix is what puts them in chronological
// order — name a new file for the day it is added.
var steps []Step

// Run applies any not-yet-applied steps in order.
func Run(ctx context.Context, db *gorm.DB, version string, opts Options) error {
	_ = version
	_ = opts

	for _, step := range steps {
		var count int64
		if err := db.Model(&models.UpgradeStep{}).
			Where("name = ?", step.Name).Count(&count).Error; err != nil {
			return fmt.Errorf("checking upgrade step %q: %w", step.Name, err)
		}
		if count > 0 {
			continue
		}

		logger.Info("applying upgrade step", "name", step.Name, "version", step.Version)
		if err := step.Run(ctx, db); err != nil {
			return fmt.Errorf("upgrade step %q failed: %w", step.Name, err)
		}

		record := &models.UpgradeStep{Name: step.Name, Version: step.Version, AppliedAt: time.Now()}
		if err := db.Create(record).Error; err != nil {
			return fmt.Errorf("recording upgrade step %q: %w", step.Name, err)
		}
	}
	return nil
}
