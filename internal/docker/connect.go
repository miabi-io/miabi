// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"

	"github.com/moby/moby/client"
)

// TLSMaterial holds PEM-encoded TLS material for a TCP Docker endpoint. A nil
// *TLSMaterial means a plaintext connection. CACert alone enables server
// verification; Cert+Key add client authentication (mTLS).
type TLSMaterial struct {
	CACert []byte
	Cert   []byte
	Key    []byte
}

func (m *TLSMaterial) config() (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if len(m.CACert) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(m.CACert) {
			return nil, errors.New("invalid CA certificate PEM")
		}
		cfg.RootCAs = pool
	}
	if len(m.Cert) > 0 && len(m.Key) > 0 {
		crt, err := tls.X509KeyPair(m.Cert, m.Key)
		if err != nil {
			return nil, err
		}
		cfg.Certificates = []tls.Certificate{crt}
	}
	return cfg, nil
}

// NewSocket connects to a Docker engine at a socket/host string (e.g.
// "unix:///var/run/docker.sock" or "tcp://host:2375" without TLS).
func NewSocket(host string) (Client, error) {
	cli, err := client.New(client.WithHost(host))
	if err != nil {
		return nil, err
	}
	return &engineClient{cli: cli}, nil
}

// NewTCP connects to a Docker engine over TCP. With tlsm == nil the connection
// is plaintext; otherwise it uses TLS (server verification, plus client auth if
// Cert+Key are present). host is e.g. "tcp://host:2376".
func NewTCP(host string, tlsm *TLSMaterial) (Client, error) {
	if tlsm == nil {
		return NewSocket(host)
	}
	tlsCfg, err := tlsm.config()
	if err != nil {
		return nil, err
	}
	httpc := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	// WithHTTPClient first so our TLS transport survives WithHost's
	// ConfigureTransport (which only installs a tcp dialer); WithScheme forces
	// HTTPS over the TCP endpoint.
	cli, err := client.New(
		client.WithHTTPClient(httpc),
		client.WithHost(host),
		client.WithScheme("https"),
	)
	if err != nil {
		return nil, err
	}
	return &engineClient{cli: cli}, nil
}
