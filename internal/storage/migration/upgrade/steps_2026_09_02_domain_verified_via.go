// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package upgrade

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

func domainVerifiedViaStep(ctx context.Context, db *gorm.DB) error {
	if !db.Migrator().HasColumn(&domainRow{}, "verified_via") {
		return nil
	}
	err := db.WithContext(ctx).Exec(`UPDATE domains
		SET verified_via = 'dns_provider'
		WHERE verified = true AND dns_provider_id IS NOT NULL`).Error
	if err != nil {
		return fmt.Errorf("backfill domains.verified_via: %w", err)
	}
	return nil
}

// domainRow names the table for the migrator without importing models.
type domainRow struct{}

func (domainRow) TableName() string { return "domains" }

func init() {
	steps = append(steps, Step{
		Name:    "domain_verified_via_backfill",
		Version: "1.9.4",
		Run:     domainVerifiedViaStep,
	})
}
