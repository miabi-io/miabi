// Copyright (c) 2026 Jonas Kaninda. All rights reserved.
//
// This file is part of Miabi Enterprise Edition.
//
// This file is licensed under the Miabi Enterprise License, not the GNU Affero
// General Public License. You may use it only with a valid Miabi Enterprise
// subscription and under the terms in internal/enterprise/LICENSE.md.
//
// SPDX-License-Identifier: LicenseRef-Miabi-Enterprise

// Package license verifies Ed25519-signed tokens offline; keep in step with the portal's pkg/license, which signs.
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

// Product is the token prefix and product claim Miabi accepts; Version is the format.
const (
	Product = "miabi"
	Version = "v1"
)

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
	ErrUnknownKey   = errors.New("license: signed by a key this build does not trust")
	ErrWrongProduct = errors.New("license: issued for another product")
	ErrBadKeyring   = errors.New("license: invalid keyring")
)

type Claims struct {
	Product   string         `json:"product"`
	LicenseID string         `json:"license_id"`
	Customer  string         `json:"customer"`
	Edition   string         `json:"edition"`        // "enterprise"
	Tier      string         `json:"tier,omitempty"` // commercial plan label (professional|business|enterprise)
	InstallID string         `json:"install_id,omitempty"`
	URL       string         `json:"url,omitempty"`
	Flags     []string       `json:"flags,omitempty"`
	Limits    map[string]int `json:"limits,omitempty"`
	Trial     bool           `json:"trial,omitempty"`
	NotBefore time.Time      `json:"not_before"`
	NotAfter  time.Time      `json:"not_after"`
	GraceDays int            `json:"grace_days"`
	IssuedAt  time.Time      `json:"issued_at"`
}

var b64 = base64.RawURLEncoding

// Keyring holds the public keys this build trusts, by key id.
type Keyring struct {
	Product string
	Keys    map[string]ed25519.PublicKey
}

type keyringDoc struct {
	Product string            `json:"product"`
	Keys    map[string]string `json:"keys"`
}

func ParseKeyring(raw string) (Keyring, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Keyring{}, ErrBadKeyring
	}
	data := []byte(raw)
	if !strings.HasPrefix(raw, "{") {
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return Keyring{}, ErrBadKeyring
		}
		data = decoded
	}
	var doc keyringDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return Keyring{}, ErrBadKeyring
	}
	ring := Keyring{Product: doc.Product, Keys: map[string]ed25519.PublicKey{}}
	for kid, pub := range doc.Keys {
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pub))
		if err != nil || len(key) != ed25519.PublicKeySize {
			return Keyring{}, fmt.Errorf("%w: key %q", ErrBadKeyring, kid)
		}
		ring.Keys[kid] = ed25519.PublicKey(key)
	}
	if ring.Product == "" || len(ring.Keys) == 0 {
		return Keyring{}, ErrBadKeyring
	}
	return ring, nil
}

// KeyID returns the kid a token names, without verifying anything.
func KeyID(token string) string {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 4 {
		return ""
	}
	return parts[1]
}

func Verify(ring Keyring, token string) (Claims, error) {
	var c Claims
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 4 || parts[0] != Product+"-"+Version || parts[1] == "" {
		return c, ErrMalformed
	}
	if ring.Product != Product {
		return c, ErrWrongProduct
	}
	key, ok := ring.Keys[parts[1]]
	if !ok {
		return c, ErrUnknownKey
	}
	payload, err := b64.DecodeString(parts[2])
	if err != nil {
		return c, ErrMalformed
	}
	sig, err := b64.DecodeString(parts[3])
	if err != nil {
		return c, ErrMalformed
	}
	if !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]+"."+parts[2]), sig) {
		return c, ErrBadSignature
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return c, fmt.Errorf("%w: bad claims", ErrMalformed)
	}
	if c.Product != Product {
		return c, ErrWrongProduct
	}
	return c, nil
}

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
