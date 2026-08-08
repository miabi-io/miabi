// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package acme issues TLS certificates via ACME DNS-01, backed by go-acme/lego. The challenge is
// solved through caller-supplied Present/CleanUp funcs, so any DNS backend can solve it. Miabi
// runs this for wildcard/managed certs; Goma still handles default HTTP-01 globally.
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

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
)

// Let's Encrypt directory URLs (the supported CAs). Staging is used for tests so
// issuance never burns production rate limits.
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

// SolverFuncs solve the DNS-01 challenge by writing/removing the _acme-challenge
// TXT (fqdn = "_acme-challenge.<domain>.", value = the challenge digest).
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

// Register creates and registers a new ACME account with the CA, returning the
// registration resource to persist alongside the key.
func Register(caDirURL, email string, key crypto.PrivateKey) (*registration.Resource, error) {
	client, err := newClient(caDirURL, &Account{Email: email, Key: key})
	if err != nil {
		return nil, err
	}
	return client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
}

// Obtain issues a certificate for the given domains using the account, solving
// DNS-01 via the supplied solver. Returns the leaf+chain PEM and the private-key
// PEM. ctx bounds the whole operation (DNS propagation + validation).
func Obtain(ctx context.Context, caDirURL string, acc *Account, domains []string, solver SolverFuncs) (certPEM, keyPEM string, err error) {
	client, err := newClient(caDirURL, acc)
	if err != nil {
		return "", "", err
	}
	if err := client.Challenge.SetDNS01Provider(&dnsProvider{ctx: ctx, solver: solver}); err != nil {
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
