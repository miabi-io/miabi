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

// Package acme issues TLS certificates via ACME DNS-01 using caller-supplied solver funcs.
package acme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
)

// Staging is for tests so issuance never burns production rate limits.
const (
	DirectoryProduction = lego.LEDirectoryProduction
	DirectoryStaging    = lego.LEDirectoryStaging
)

// Account is the platform ACME account used for all issuance.
type Account struct {
	Email        string
	Key          crypto.PrivateKey
	Registration *registration.Resource
}

type acmeUser struct{ acc *Account }

func (u *acmeUser) GetEmail() string                        { return u.acc.Email }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.acc.Registration }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.acc.Key }

// SolverFuncs write and remove the _acme-challenge TXT record (fqdn has a trailing dot).
type SolverFuncs struct {
	Present func(ctx context.Context, fqdn, value string) error
	CleanUp func(ctx context.Context, fqdn, value string) error
}

// GenerateAccountKey returns a fresh ECDSA P-256 account key.
func GenerateAccountKey() (crypto.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

// EncodeKey PEM-encodes an ECDSA account key for storage.
func EncodeKey(key crypto.PrivateKey) (string, error) {
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return "", errors.New("acme: account key is not ECDSA")
	}
	der, err := x509.MarshalECPrivateKey(ec)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})), nil
}

// DecodeKey parses a PEM-encoded ECDSA account key.
func DecodeKey(pemStr string) (crypto.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("acme: invalid account key PEM")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

// EncodeRegistration / DecodeRegistration persist the account registration.
func EncodeRegistration(reg *registration.Resource) (string, error) {
	b, err := json.Marshal(reg)
	return string(b), err
}

func DecodeRegistration(s string) (*registration.Resource, error) {
	if s == "" {
		return nil, nil
	}
	var reg registration.Resource
	if err := json.Unmarshal([]byte(s), &reg); err != nil {
		return nil, err
	}
	return &reg, nil
}

func Register(caDirURL, email string, key crypto.PrivateKey) (*registration.Resource, error) {
	client, err := newClient(caDirURL, &Account{Email: email, Key: key})
	if err != nil {
		return nil, err
	}
	return client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
}

// Obtain issues a certificate via DNS-01; propagationTimeout only caps lego's polling, it never adds a fixed wait.
func Obtain(ctx context.Context, caDirURL string, acc *Account, domains []string, solver SolverFuncs, propagationTimeout time.Duration) (certPEM, keyPEM string, err error) {
	client, err := newClient(caDirURL, acc)
	if err != nil {
		return "", "", err
	}
	var opts []dns01.ChallengeOption
	if propagationTimeout > 0 {
		opts = append(opts, dns01.AddDNSTimeout(propagationTimeout))
	}
	if err := client.Challenge.SetDNS01Provider(&dnsProvider{ctx: ctx, solver: solver}, opts...); err != nil {
		return "", "", fmt.Errorf("acme: set dns-01 provider: %w", err)
	}
	res, err := client.Certificate.Obtain(certificate.ObtainRequest{Domains: domains, Bundle: true})
	if err != nil {
		return "", "", fmt.Errorf("acme: obtain certificate: %w", err)
	}
	return string(res.Certificate), string(res.PrivateKey), nil
}

func newClient(caDirURL string, acc *Account) (*lego.Client, error) {
	cfg := lego.NewConfig(&acmeUser{acc: acc})
	if caDirURL != "" {
		cfg.CADirURL = caDirURL
	}
	cfg.Certificate.KeyType = certcrypto.RSA2048
	return lego.NewClient(cfg)
}

type dnsProvider struct {
	ctx    context.Context
	solver SolverFuncs
}

var _ challenge.Provider = (*dnsProvider)(nil)

func (p *dnsProvider) Present(domain, token, keyAuth string) error {
	fqdn, value := dns01.GetRecord(domain, keyAuth)
	return p.solver.Present(p.ctx, fqdn, value)
}

func (p *dnsProvider) CleanUp(domain, token, keyAuth string) error {
	fqdn, value := dns01.GetRecord(domain, keyAuth)
	return p.solver.CleanUp(p.ctx, fqdn, value)
}
