// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package siem streams the audit log to external SIEM sinks at-least-once, gated on the siem_stream
// entitlement and entirely off the request path. Durability comes from a per-target cursor: the
// cursor advances only on a successful ship, so an outage or restart never drops events.
package siem

import (
	"context"
	"fmt"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

// Flag is the entitlement that unlocks SIEM streaming.
const Flag = "siem_stream"

const batchSize = 500

// EntitlementChecker is satisfied by enterprise.EE; kept minimal so this package
// does not import the enterprise seam.
type EntitlementChecker interface{ Has(flag string) bool }

// Sink ships a batch of audit events to an external system. Returning an error
// leaves the cursor un-advanced so the batch is retried next tick.
type Sink interface {
	Ship(events []models.AuditLog) error
}

// Streamer ships audit events to all enabled SIEM targets.
type Streamer struct {
	audit  *repositories.AuditLogRepository
	config *repositories.SIEMConfigRepository
	ee     EntitlementChecker
}

func NewStreamer(audit *repositories.AuditLogRepository, config *repositories.SIEMConfigRepository, ee EntitlementChecker) *Streamer {
	return &Streamer{audit: audit, config: config, ee: ee}
}

// Enabled reports whether streaming is licensed.
func (s *Streamer) Enabled() bool { return s.ee != nil && s.ee.Has(Flag) }

// Tick ships any new audit events to every enabled target. Safe to call on a
// schedule and on an eventbus nudge; it is a no-op when streaming is not
// entitled. One target's failure never affects another.
func (s *Streamer) Tick(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	configs, err := s.config.FindEnabled()
	if err != nil {
		return err
	}
	for i := range configs {
		s.shipOne(&configs[i])
	}
	return nil
}

// shipOne drains the backlog for a single target in batches until caught up or a
// batch fails. The cursor advances only after a confirmed ship.
func (s *Streamer) shipOne(cfg *models.SIEMConfig) {
	sink, err := s.sinkFor(cfg)
	if err != nil {
		_ = s.config.RecordError(cfg.ID, err.Error())
		return
	}
	cursor := cfg.LastShippedID
	for {
		rows, err := s.audit.Since(cursor, batchSize)
		if err != nil {
			_ = s.config.RecordError(cfg.ID, err.Error())
			return
		}
		if len(rows) == 0 {
			return
		}
		if err := sink.Ship(rows); err != nil {
			logger.Warn("siem: ship failed", "target", cfg.Name, "error", err)
			_ = s.config.RecordError(cfg.ID, err.Error())
			return // keep the cursor; retry next tick (at-least-once)
		}
		cursor = rows[len(rows)-1].ID
		if err := s.config.AdvanceCursor(cfg.ID, cursor); err != nil {
			logger.Warn("siem: cursor advance failed", "target", cfg.Name, "error", err)
			return
		}
		if len(rows) < batchSize {
			return // caught up
		}
	}
}

// Test ships a single synthetic event to verify a target's connectivity.
func (s *Streamer) Test(cfg *models.SIEMConfig, event models.AuditLog) error {
	sink, err := s.sinkFor(cfg)
	if err != nil {
		return err
	}
	return sink.Ship([]models.AuditLog{event})
}

func (s *Streamer) sinkFor(cfg *models.SIEMConfig) (Sink, error) {
	switch cfg.Sink {
	case models.SIEMSinkSyslog:
		return newSyslogSink(cfg)
	case models.SIEMSinkWebhook:
		auth := ""
		if cfg.AuthHeaderEnc != "" {
			if v, err := crypto.Decrypt(cfg.AuthHeaderEnc); err == nil {
				auth = v
			}
		}
		return newWebhookSink(cfg.Endpoint, auth), nil
	default:
		return nil, fmt.Errorf("unsupported siem sink: %s", cfg.Sink)
	}
}
