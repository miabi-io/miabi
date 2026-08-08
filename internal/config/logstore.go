// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/logstore"
)

// BuildLogStore constructs the shared execution-log store from configuration. It returns a
// disabled (nil) store when the backend is "off" or the directory can't be prepared — the
// store is never a hard boot dependency, so producers fall back to DB-tail-only behavior.
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
