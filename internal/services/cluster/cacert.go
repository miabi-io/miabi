// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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

// Asking an operator to paste a PEM is asking them to go and find one, and the most
// likely outcome is they give up and pick "skip verification" instead — which is the
// option we least want them to take.
//
// The control plane knows what certificate it serves. It can fetch it, hand it back,
// and let the operator confirm it. That turns "trust a custom CA" from a research task
// into one click, which is the difference between it being the common path and the
// rare one.

// ControlPlaneCert is the certificate the control plane currently serves, offered so
// the agents can be pinned to it.
type ControlPlaneCert struct {
	// PEM is what goes into MIABI_CA_CERT.
	PEM string `json:"pem"`
	// The fields below are for the operator to eyeball before trusting it. A cert
	// fetched over an unverified connection could, in principle, be an attacker's —
	// showing the subject and fingerprint is what makes the confirmation meaningful
	// rather than ceremonial.
	Subject     string `json:"subject"`
	Issuer      string `json:"issuer"`
	NotAfter    string `json:"not_after"`
	Fingerprint string `json:"fingerprint"` // SHA-256, colon-separated
	// SelfSigned is true when the certificate is its own issuer — the usual case for a
	// homelab control plane, and the case where pinning it IS trusting the CA.
	SelfSigned bool `json:"self_signed"`
	// PubliclyTrusted is true when the chain already verifies against the system pool,
	// in which case no CA needs to be distributed at all and the agents should simply
	// verify normally.
	PubliclyTrusted bool `json:"publicly_trusted"`

	// Hosts are the names the certificate actually vouches for (its SANs).
	Hosts []string `json:"hosts,omitempty"`
	// MatchesHost is whether the certificate names the address the agents will dial.
	//
	// This is the trap in "just trust the CA": adding a certificate to the trust pool
	// does NOT skip the hostname check. A certificate with no SANs — Goma's default
	// self-signed cert has none — fails with "cannot validate certificate for <host>
	// because it doesn't contain any IP SANs" no matter how well it is trusted. So a
	// CA that does not name the control plane is useless to the agents, and telling
	// the operator that up front is the difference between one confusing failure and
	// two.
	MatchesHost bool `json:"matches_host"`
	// DialHost is the address the agents dial, so the UI can name it in the failure.
	DialHost string `json:"dial_host"`

	// AnchorIsCA is whether what we can offer is a real certificate AUTHORITY (a
	// self-signed CA), or merely the server's own leaf certificate.
	//
	// A server sends its leaf and usually its intermediates, but often not the root —
	// and a server behind a private CA frequently sends the leaf alone. Pinning that
	// leaf works, right up until the certificate is renewed: the new leaf is a
	// different certificate, nothing trusts it, and every agent drops off at once.
	//
	// So when this is false, the honest advice is "paste your actual CA instead" —
	// otherwise we hand the operator a trust anchor with an expiry date they did not
	// agree to.
	AnchorIsCA bool `json:"anchor_is_ca"`
}

// ErrNoTLS is returned when the control plane is not served over TLS, so there is no
// certificate to trust (and the agents need none).
var ErrNoTLS = errors.New("the control plane is not served over HTTPS; agents need no CA")

// FetchControlPlaneCert dials the control plane and returns the certificate it serves.
//
// The dial deliberately skips verification: the whole point is to reach a control
// plane whose certificate does NOT verify yet. That is safe here because we are not
// trusting the connection — we are collecting a certificate for a human to confirm,
// and showing them its fingerprint so they can.
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
	// Pin the ROOT of the chain, not the leaf: a leaf is reissued on every renewal and
	// would break every agent when it rotates. For a self-signed cert the leaf IS the
	// root, so this is the same certificate.
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
		// Trusting the CA does not skip the hostname check: a certificate that does not
		// name this host is useless to the agents however well it is trusted.
		MatchesHost: leaf.VerifyHostname(u.Hostname()) == nil,
		// A durable anchor is a self-signed CA. Anything else is a leaf, and a leaf
		// stops being trusted the moment it is reissued.
		AnchorIsCA: root.IsCA && root.Subject.String() == root.Issuer.String(),
	}
	// If it already verifies against the system pool, distributing a CA is pointless —
	// say so, so the operator picks "verify" rather than pinning something they do not
	// need to pin.
	if _, verr := leaf.Verify(x509.VerifyOptions{DNSName: u.Hostname()}); verr == nil {
		out.PubliclyTrusted = true
	}
	return out, nil
}

// certHosts lists the names a certificate vouches for. Empty means it vouches for
// none — which is exactly the case that makes trusting it pointless.
func certHosts(c *x509.Certificate) []string {
	hosts := append([]string{}, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		hosts = append(hosts, ip.String())
	}
	return hosts
}

// fingerprint renders the certificate's SHA-256 as colon-separated hex — the form an
// operator can compare against `openssl x509 -fingerprint -sha256`.
func fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	parts := make([]string, 0, len(sum))
	for _, b := range sum {
		parts = append(parts, fmt.Sprintf("%02X", b))
	}
	return strings.Join(parts, ":")
}
