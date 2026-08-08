// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// SIEM sink kinds.
const (
	SIEMSinkSyslog  = "syslog"
	SIEMSinkWebhook = "webhook"
	SIEMSinkS3      = "s3"
)

// SIEM payload formats.
const (
	SIEMFormatJSON = "json" // NDJSON
	SIEMFormatCEF  = "cef"  // syslog CEF
)

// SIEMConfig is one external audit-streaming target (Enterprise, gated on siem_stream). The
// streamer ships audit events at-least-once and records progress in LastShippedID, a durable
// cursor surviving restarts and sink outages. The table is empty in Community.
type SIEMConfig struct {
	ID       uint   `json:"id" gorm:"primaryKey"`
	Name     string `json:"name" gorm:"not null"`
	Sink     string `json:"sink" gorm:"not null"`         // syslog | webhook | s3
	Endpoint string `json:"endpoint"`                     // syslog addr / webhook URL / s3 bucket+prefix
	Format   string `json:"format" gorm:"default:'json'"` // json | cef
	// AuthHeaderEnc is a webhook Authorization value, encrypted at rest. Never
	// serialized.
	AuthHeaderEnc string `json:"-" gorm:"column:auth_header"`
	Enabled       bool   `json:"enabled" gorm:"default:true;not null"`

	// LastShippedID is the durable cursor: the highest audit-log id confirmed
	// shipped to this sink. The streamer only advances it after a successful ship.
	LastShippedID uint       `json:"last_shipped_id"`
	LastError     string     `json:"last_error"`
	LastShippedAt *time.Time `json:"last_shipped_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IsValidSIEMSink reports whether s is a supported sink kind.
func IsValidSIEMSink(s string) bool {
	switch s {
	case SIEMSinkSyslog, SIEMSinkWebhook, SIEMSinkS3:
		return true
	default:
		return false
	}
}
