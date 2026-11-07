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

// Package managedcert issues and renews certificates via ACME DNS-01; Goma still serves them as custom certs.
package managedcert

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/acme"
	"github.com/miabi-io/miabi/internal/dns"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/certificate"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/dnsprovider"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrDomainNotFound    = errors.New("domain not found")
	ErrDomainNotVerified = errors.New("domain ownership is not verified")
	ErrNoProvider        = errors.New("domain has no connected DNS provider")
)

const (
	// issueTimeout bounds one issuance (DNS propagation + ACME validation).
	issueTimeout = 5 * time.Minute
	// challengeTTL is the TTL of the _acme-challenge TXT records we write.
	challengeTTL = 60 * time.Second
)

// Service issues + renews managed certificates.
type Service struct {
	certs        *certificate.Service
	dnsProviders *dnsprovider.Service
	domains      *repositories.DomainRepository
	accounts     *repositories.ACMEAccountRepository
	quota        *quota.Service
	email        string
	caDirURL     string
	renewWithin  time.Duration
}

func NewService(
	certs *certificate.Service,
	dnsProviders *dnsprovider.Service,
	domains *repositories.DomainRepository,
	accounts *repositories.ACMEAccountRepository,
	email, caDirURL string,
	renewWithin time.Duration,
) *Service {
	if caDirURL == "" {
		caDirURL = acme.DirectoryProduction
	}
	return &Service{
		certs: certs, dnsProviders: dnsProviders, domains: domains, accounts: accounts,
		email: email, caDirURL: caDirURL, renewWithin: renewWithin,
	}
}

func (s *Service) SetQuota(q *quota.Service) { s.quota = q }

// Request returns the "issuing" row immediately and issues in the background; callers poll its status.
func (s *Service) Request(workspaceID, domainID uint, name string, includeWildcard, autoRenew bool) (*models.Certificate, error) {
	if s.quota.Enabled() {
		if err := s.quota.Require(workspaceID, quota.CapDNSProviders); err != nil {
			return nil, err
		}
	}
	d, err := s.domains.FindInWorkspace(workspaceID, domainID)
	if err != nil {
		return nil, ErrDomainNotFound
	}
	if !d.Verified {
		return nil, ErrDomainNotVerified
	}
	if d.DNSProviderID == nil {
		return nil, ErrNoProvider
	}
	names := []string{d.Name}
	if includeWildcard {
		names = append(names, "*."+d.Name)
	}
	if strings.TrimSpace(name) == "" {
		name = d.Name
	}
	cert, err := s.certs.BeginManaged(workspaceID, name, *d.DNSProviderID, autoRenew)
	if err != nil {
		return nil, err
	}
	go s.issue(workspaceID, cert.ID, names, d.DNSProviderID)
	return cert, nil
}

// Sequential since there are few.
func (s *Service) RenewDue() {
	due, err := s.certs.ListManagedDue(s.renewWithin)
	if err != nil {
		logger.Warn("managedcert: list due failed", "error", err)
		return
	}
	for i := range due {
		c := due[i]
		if c.Status == models.CertStatusIssuing {
			continue // already in flight
		}
		logger.Info("managedcert: renewing", "cert", c.ID, "name", c.Name, "not_after", c.NotAfter)
		if _, err := s.certs.BeginManaged(c.WorkspaceID, c.Name, providerID(c), c.AutoRenew); err != nil {
			logger.Warn("managedcert: begin renew failed", "cert", c.ID, "error", err)
			continue
		}
		s.issue(c.WorkspaceID, c.ID, c.DNSNames, c.DNSProviderID)
	}
}

func (s *Service) issue(workspaceID, certID uint, names []string, providerID *uint) {
	ctx, cancel := context.WithTimeout(context.Background(), issueTimeout)
	defer cancel()

	zone, prov, propagation, err := s.resolveZoneAndProvider(workspaceID, names, providerID)
	if err != nil {
		s.fail(workspaceID, certID, err)
		return
	}
	acct, err := s.account()
	if err != nil {
		s.fail(workspaceID, certID, fmt.Errorf("acme account: %w", err))
		return
	}
	solver := acme.SolverFuncs{
		Present: func(ctx context.Context, fqdn, value string) error {
			return prov.SetRecord(ctx, zone, dns.Record{Type: "TXT", Name: strings.TrimSuffix(fqdn, "."), Value: value, TTL: challengeTTL})
		},
		CleanUp: func(ctx context.Context, fqdn, value string) error {
			return prov.DeleteRecord(ctx, zone, dns.Record{Type: "TXT", Name: strings.TrimSuffix(fqdn, "."), Value: value})
		},
	}
	certPEM, keyPEM, err := acme.Obtain(ctx, s.caDirURL, acct, names, solver, propagation)
	if err != nil {
		s.fail(workspaceID, certID, err)
		return
	}
	if _, err := s.certs.CompleteManaged(workspaceID, certID, certPEM, keyPEM); err != nil {
		s.fail(workspaceID, certID, fmt.Errorf("store certificate: %w", err))
		return
	}
	logger.Info("managedcert: issued", "cert", certID, "names", names)
}

func (s *Service) fail(workspaceID, certID uint, cause error) {
	logger.Warn("managedcert: issuance failed", "cert", certID, "error", cause)
	_ = s.certs.FailManaged(workspaceID, certID, cause.Error())
}

func (s *Service) resolveZoneAndProvider(workspaceID uint, names []string, providerID *uint) (string, dns.Provider, time.Duration, error) {
	if len(names) == 0 {
		return "", nil, 0, ErrDomainNotFound
	}
	base := strings.ToLower(strings.TrimPrefix(names[0], "*."))
	doms, err := s.domains.ListByWorkspace(workspaceID)
	if err != nil {
		return "", nil, 0, err
	}
	var zone *models.Domain
	for i := range doms {
		n := strings.ToLower(doms[i].Name)
		if base == n || strings.HasSuffix(base, "."+n) {
			if zone == nil || len(n) > len(zone.Name) {
				zone = &doms[i]
			}
		}
	}
	if zone == nil {
		return "", nil, 0, ErrDomainNotFound
	}
	pid := providerID
	if pid == nil {
		pid = zone.DNSProviderID
	}
	if pid == nil {
		return "", nil, 0, ErrNoProvider
	}
	prov, err := s.dnsProviders.Provider(workspaceID, *pid)
	if err != nil {
		return "", nil, 0, err
	}
	return zone.Name, prov, s.dnsProviders.PropagationTimeout(workspaceID, *pid), nil
}

// The account key is encrypted at rest.
func (s *Service) account() (*acme.Account, error) {
	if a, err := s.accounts.FindByCA(s.caDirURL); err == nil {
		keyPEM, derr := crypto.Decrypt(a.KeyEnc)
		if derr != nil {
			return nil, derr
		}
		key, kerr := acme.DecodeKey(keyPEM)
		if kerr != nil {
			return nil, kerr
		}
		reg, rerr := acme.DecodeRegistration(a.RegistrationJSON)
		if rerr != nil {
			return nil, rerr
		}
		return &acme.Account{Email: a.Email, Key: key, Registration: reg}, nil
	}
	// First use: generate a key, register with the CA, and persist.
	key, err := acme.GenerateAccountKey()
	if err != nil {
		return nil, err
	}
	reg, err := acme.Register(s.caDirURL, s.email, key)
	if err != nil {
		return nil, err
	}
	keyPEM, err := acme.EncodeKey(key)
	if err != nil {
		return nil, err
	}
	keyEnc, err := crypto.Encrypt(keyPEM)
	if err != nil {
		return nil, err
	}
	regJSON, _ := acme.EncodeRegistration(reg)
	if cerr := s.accounts.Create(&models.ACMEAccount{
		CADirURL: s.caDirURL, Email: s.email, KeyEnc: keyEnc, RegistrationJSON: regJSON,
	}); cerr != nil {
		logger.Warn("managedcert: persist acme account failed", "error", cerr)
	}
	return &acme.Account{Email: s.email, Key: key, Registration: reg}, nil
}

func providerID(c models.Certificate) uint {
	if c.DNSProviderID == nil {
		return 0
	}
	return *c.DNSProviderID
}
