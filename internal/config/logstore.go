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

package config

import (
	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/logstore"
)

// BuildLogStore returns nil when disabled or the dir can't be prepared; the store is never a hard boot dependency.
func (c *Config) BuildLogStore() *logstore.Store {
	l := c.LogStore
	if !l.Enabled() {
		logger.Info("log store disabled (MIABI_LOG_BACKEND=off): logs kept as a bounded DB tail only")
		return nil
	}
	cfg := logstore.Config{
		MaxBytes:      l.MaxBytes,
		TailBytes:     l.TailBytes,
		Compress:      l.Compression != "none",
		RetentionDays: l.RetentionDays,
	}
	switch l.Backend {
	case "filesystem", "fs", "":
		be, err := logstore.NewFSBackend(l.Dir)
		if err != nil {
			logger.Error("log store: filesystem backend unavailable; falling back to DB-tail-only", "dir", l.Dir, "error", err)
			return nil
		}
		logger.Info("log store enabled", "backend", "filesystem", "dir", l.Dir, "retention_days", l.RetentionDays)
		return logstore.New(be, cfg)
	default:
		logger.Warn("log store: unknown MIABI_LOG_BACKEND; falling back to DB-tail-only", "backend", l.Backend)
		return nil
	}
}
