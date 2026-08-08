// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package license implements Miabi's offline, signed commercial license token: an Ed25519-signed
// claims blob verified against a public key embedded in the binary, so verification works
// air-gapped. It is pure (no DB, no build tag); a CE binary simply never imports it.
package license

import (
	"crypto/ed25519"
	"crypto/rand"
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

var (
	// ErrMalformed is returned for a token that is not three dot-separated parts
	// with the expected prefix.
	ErrMalformed = errors.New("license: malformed token")
	// ErrBadSignature is returned when the signature does not verify against the
	// public key — a tampered or forged token.
	ErrBadSignature = errors.New("license: signature verification failed")
	// ErrBadKey is returned when a supplied key is not a valid base64 Ed25519 key.
	ErrBadKey = errors.New("license: invalid key")
)

// Claims is the signed license payload. Limits use the platform-wide convention
// (-1 = unlimited, 0 = none, N = N), mirroring the quota engine.
type Claims struct {
	LicenseID string `json:"license_id"`
	Customer  string `json:"customer"`
	Edition   string `json:"edition"`        // "enterprise"
	Tier      string `json:"tier,omitempty"` // commercial plan label (professional|business|enterprise)
	// InstallID binds the license to one specific instance by its stable Install ID
	// (the strong, primary binding — a customer supplies it at purchase). Empty =
	// not bound by install id.
	InstallID string `json:"install_id,omitempty"`
	// URL optionally also binds by deployment host. Empty = not bound by URL. A
	// license with neither InstallID nor URL is unlimited (any instance).
	URL       string         `json:"url,omitempty"`
	Flags     []string       `json:"flags"`
	Limits    map[string]int `json:"limits"`
	NotBefore time.Time      `json:"not_before"`
	NotAfter  time.Time      `json:"not_after"`
	GraceDays int            `json:"grace_days"`
	IssuedAt  time.Time      `json:"issued_at"`
}

var b64 = base64.RawURLEncoding

// GenerateKey returns a fresh Ed25519 keypair as base64 strings: the private key
// (kept secret in the issuer) and the public key (embedded in the binary).
func GenerateKey() (publicKey, privateKey string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(pub), base64.StdEncoding.EncodeToString(priv), nil
}

// Sign serializes the claims and returns a signed token of the form
// "miabi-v1.<b64(claims)>.<b64(sig)>". privateKeyB64 is the issuer's secret key.
func Sign(privateKeyB64 string, c Claims) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(privateKeyB64))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return "", ErrBadKey
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	body := tokenPrefix + "." + b64.EncodeToString(payload)
	sig := ed25519.Sign(ed25519.PrivateKey(raw), []byte(body))
	return body + "." + b64.EncodeToString(sig), nil
}

// Verify checks the token's signature against publicKeyB64 and returns its
// claims. It does NOT judge expiry — the caller resolves state via Evaluate, so
// an expired but authentic token still parses (needed for the grace/degrade UX).
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

// State is the lifecycle position of an installed license, evaluated against the
// current time. A lapsed license degrades paid features to read-only; it never
// stops the open-source core.
type State string

const (
	StateValid    State = "valid"    // within term: full function
	StateGrace    State = "grace"    // past expiry, within grace days: full function + warning
	StateDegraded State = "degraded" // past grace: paid features read-only
	StateNone     State = "none"     // no/invalid/not-yet-active license: community
)

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

// Evaluate resolves claims into a Snapshot at time now. A token whose NotBefore
// is in the future is treated as not-yet-active (StateNone).
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
