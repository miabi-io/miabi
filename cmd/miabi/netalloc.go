// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/services/netalloc"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/gorm"
)

// newSubnetAllocator builds the subnet allocator for the worker's deploy/job handlers.
// Returns nil on a config error, so they fall back to Docker's default address pool.
func newSubnetAllocator(cfg *config.Config, db *gorm.DB) *netalloc.Service {
	a, err := netalloc.NewService(repositories.NewNetworkAllocationRepository(db), cfg.NetworkPoolCIDR, cfg.NetworkSubnetPrefix)
	if err != nil {
		logger.Warn("subnet allocator disabled in worker; falling back to Docker's default address pool", "error", err)
		return nil
	}
	return a
}
