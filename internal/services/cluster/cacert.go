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

package cluster

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// Offering the served cert for one-click confirmation beats operators giving up and skipping verification.

type ControlPlaneCert struct {
	// PEM is what goes into MIABI_CA_CERT.
	PEM string `json:"pem"`
	// A cert fetched unverified could be an attacker's; subject and fingerprint make confirmation meaningful.
	Subject     string `json:"subject"`
	Issuer      string `json:"issuer"`
	NotAfter    string `json:"not_after"`
	Fingerprint string `json:"fingerprint"` // SHA-256, colon-separated
	// For a self-signed cert, pinning it is trusting the CA.
	SelfSigned bool `json:"self_signed"`
	// Then no CA needs distributing and agents should simply verify normally.
	PubliclyTrusted bool `json:"publicly_trusted"`

	// Hosts are the names the certificate actually vouches for (its SANs).
	Hosts []string `json:"hosts,omitempty"`
	// Trusting a cert does not skip the hostname check, so one that doesn't name the control plane is useless.
	MatchesHost bool `json:"matches_host"`
	// DialHost is the address the agents dial, so the UI can name it in the failure.
	DialHost string `json:"dial_host"`

	// A pinned leaf breaks every agent at renewal; when false, advise pasting the real CA.
	AnchorIsCA bool `json:"anchor_is_ca"`
}

var ErrNoTLS = errors.New("the control plane is not served over HTTPS; agents need no CA")

// The dial skips verification on purpose; safe because a human confirms the cert by fingerprint.
func (s *Service) FetchControlPlaneCert(ctx context.Context) (ControlPlaneCert, error) {
	raw := strings.TrimSpace(s.controlURL)
	if raw == "" {
		return ControlPlaneCert{}, ErrControlURLRequired
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ControlPlaneCert{}, err
	}
	if u.Scheme != "https" {
		return ControlPlaneCert{}, ErrNoTLS
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "443")
	}

	dialer := &net.Dialer{Timeout: 8 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", host,
		&tls.Config{InsecureSkipVerify: true, ServerName: u.Hostname()}) //nolint:gosec // see doc comment
	if err != nil {
		return ControlPlaneCert{}, err
	}
	defer func() { _ = conn.Close() }()

	chain := conn.ConnectionState().PeerCertificates
	if len(chain) == 0 {
		return ControlPlaneCert{}, errors.New("the control plane presented no certificate")
	}
	// Pin the root: a leaf is reissued on renewal and would break every agent.
	root := chain[len(chain)-1]

	leaf := chain[0]
	out := ControlPlaneCert{
		PEM:         string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw})),
		Subject:     root.Subject.String(),
		Issuer:      root.Issuer.String(),
		NotAfter:    root.NotAfter.UTC().Format(time.RFC3339),
		Fingerprint: fingerprint(root),
		SelfSigned:  root.Subject.String() == root.Issuer.String(),
		DialHost:    u.Hostname(),
		Hosts:       certHosts(leaf),
		// Trusting the CA does not skip the hostname check.
		MatchesHost: leaf.VerifyHostname(u.Hostname()) == nil,
		// A leaf stops being trusted the moment it is reissued.
		AnchorIsCA: root.IsCA && root.Subject.String() == root.Issuer.String(),
	}
	// Already publicly trusted, so the operator should pick "verify" rather than pin.
	if _, verr := leaf.Verify(x509.VerifyOptions{DNSName: u.Hostname()}); verr == nil {
		out.PubliclyTrusted = true
	}
	return out, nil
}

// Empty means it vouches for no name, which makes trusting it pointless.
func certHosts(c *x509.Certificate) []string {
	hosts := append([]string{}, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		hosts = append(hosts, ip.String())
	}
	return hosts
}

// Matches the form of `openssl x509 -fingerprint -sha256`.
func fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	parts := make([]string, 0, len(sum))
	for _, b := range sum {
		parts = append(parts, fmt.Sprintf("%02X", b))
	}
	return strings.Join(parts, ":")
}
