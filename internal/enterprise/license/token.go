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

// Package license implements Miabi's offline, signed commercial license token: an Ed25519-signed
// claims blob verified against a public key embedded in the binary, so verification works
// air-gapped. It is pure (no DB, no build tag); a CE binary simply never imports it.
package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// tokenPrefix versions the wire format and is part of the signed message, so a
// token issued for one format can never be replayed under another.
const tokenPrefix = "miabi-v1"

type State string

const (
	StateValid    State = "valid"    // within term: full function
	StateGrace    State = "grace"    // past expiry, within grace days: full function + warning
	StateDegraded State = "degraded" // past grace: paid features read-only
	StateNone     State = "none"     // no/invalid/not-yet-active license: community
)

var (
	ErrMalformed    = errors.New("license: malformed token")
	ErrBadSignature = errors.New("license: signature verification failed")
	ErrBadKey       = errors.New("license: invalid key")
)

type Claims struct {
	LicenseID string         `json:"license_id"`
	Customer  string         `json:"customer"`
	Edition   string         `json:"edition"`        // "enterprise"
	Tier      string         `json:"tier,omitempty"` // commercial plan label (professional|business|enterprise)
	InstallID string         `json:"install_id,omitempty"`
	URL       string         `json:"url,omitempty"`
	Flags     []string       `json:"flags"`
	Limits    map[string]int `json:"limits"`
	NotBefore time.Time      `json:"not_before"`
	NotAfter  time.Time      `json:"not_after"`
	GraceDays int            `json:"grace_days"`
	IssuedAt  time.Time      `json:"issued_at"`
}

var b64 = base64.RawURLEncoding

// Verify checks the token's signature against publicKeyB64 and returns its
// claims.
func Verify(publicKeyB64, token string) (Claims, error) {
	var c Claims
	pubRaw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKeyB64))
	if err != nil || len(pubRaw) != ed25519.PublicKeySize {
		return c, ErrBadKey
	}
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 || parts[0] != tokenPrefix {
		return c, ErrMalformed
	}
	payload, err := b64.DecodeString(parts[1])
	if err != nil {
		return c, ErrMalformed
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return c, ErrMalformed
	}
	body := parts[0] + "." + parts[1]
	if !ed25519.Verify(ed25519.PublicKey(pubRaw), []byte(body), sig) {
		return c, ErrBadSignature
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return c, fmt.Errorf("license: bad claims: %w", err)
	}
	return c, nil
}

// Snapshot is the resolved view of a license at a point in time.
type Snapshot struct {
	State     State
	Edition   string
	Tier      string
	InstallID string
	URL       string
	Customer  string
	LicenseID string
	Flags     map[string]bool
	Limits    map[string]int
	NotAfter  time.Time
	GraceEnds time.Time
}

func Evaluate(c Claims, now time.Time) Snapshot {
	graceEnds := c.NotAfter.AddDate(0, 0, c.GraceDays)
	flags := make(map[string]bool, len(c.Flags))
	for _, f := range c.Flags {
		flags[f] = true
	}
	s := Snapshot{
		Edition: c.Edition, Tier: c.Tier, InstallID: c.InstallID, URL: c.URL,
		Customer: c.Customer, LicenseID: c.LicenseID,
		Flags: flags, Limits: c.Limits, NotAfter: c.NotAfter, GraceEnds: graceEnds,
	}
	switch {
	case !c.NotBefore.IsZero() && now.Before(c.NotBefore):
		s.State = StateNone
	case now.Before(c.NotAfter):
		s.State = StateValid
	case now.Before(graceEnds):
		s.State = StateGrace
	default:
		s.State = StateDegraded
	}
	return s
}
