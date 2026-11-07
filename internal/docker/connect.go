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

package docker

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"

	"github.com/moby/moby/client"
)

// TLSMaterial nil means plaintext; CACert alone verifies the server, Cert+Key add mTLS.
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

func NewSocket(host string) (Client, error) {
	cli, err := client.New(client.WithHost(host))
	if err != nil {
		return nil, err
	}
	return &engineClient{cli: cli}, nil
}

func NewTCP(host string, tlsm *TLSMaterial) (Client, error) {
	if tlsm == nil {
		return NewSocket(host)
	}
	tlsCfg, err := tlsm.config()
	if err != nil {
		return nil, err
	}
	httpc := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	// WithHTTPClient first so our TLS transport survives WithHost's ConfigureTransport.
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
