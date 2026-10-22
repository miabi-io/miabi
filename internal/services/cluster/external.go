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
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
)

var (
	// ErrInvalidExternalDomain is returned for an external domain that is not a DNS name.
	ErrInvalidExternalDomain = errors.New("the external domain must be a DNS name such as apps.eu-central.example.com")
	// ErrExternalDomainTaken is returned when another cluster already generates URLs under the domain.
	ErrExternalDomainTaken = errors.New("another cluster already uses this external domain")
	// ErrExternalCertNotFound is returned for a certificate that is not in the system workspace.
	ErrExternalCertNotFound = errors.New("no such certificate in the system workspace")
	// ErrExternalCertNeedsDomain is returned for a certificate chosen in a cluster without an external domain.
	ErrExternalCertNeedsDomain = errors.New("set the external domain before choosing a certificate for it")
	// ErrExternalCertMismatch is returned for a certificate without the *.<external domain> wildcard.
	ErrExternalCertMismatch = errors.New("the certificate does not cover the external domain's wildcard")
	// ErrExternalCertUnusable is returned for a certificate that is still issuing, failed or expired.
	ErrExternalCertUnusable = errors.New("the certificate is not active or has expired")
	// ErrInvalidCertProvider is returned for a certificate provider that is not a plain provider name.
	ErrInvalidCertProvider = errors.New("the certificate provider must be a provider name from the gateway configuration, e.g. letsencrypt")
	// ErrExternalAccessPinned is returned for a change to a field the environment sets.
	ErrExternalAccessPinned = errors.New("set by MIABI_EXTERNAL_BASE_DOMAIN or MIABI_EXTERNAL_BASE_PROVIDER; change it in the environment")
)

var certProviderPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

// ExternalAccess returns the wildcard domain and certificate provider for generated app URLs in a cluster; id 0 is
// the default cluster. An empty domain means one-click external access is off there.
func (s *Service) ExternalAccess(clusterID uint) (domain, provider string) {
	if s.store == nil {
		return "", ""
	}
	c, err := s.store.FindByID(clusterID)
	if err != nil {
		return "", ""
	}
	return c.ExternalBaseDomain, c.ExternalCertProvider
}

// ExternalApps counts the apps in a cluster holding generated URLs, which a domain change re-hosts.
func (s *Service) ExternalApps(clusterID uint) int64 {
	if s.store == nil {
		return 0
	}
	n, err := s.store.CountExternalApps(clusterID)
	if err != nil {
		logger.Warn("count apps with generated URLs failed", "cluster", clusterID, "error", err)
		return 0
	}
	return n
}

// SetExternalAccessListener is told when a cluster's external domain or certificate provider changed, so the
// generated app URLs follow.
func (s *Service) SetExternalAccessListener(fn func(ctx context.Context, clusterID uint)) {
	s.externalListener = fn
}

// PinExternalAccess applies MIABI_EXTERNAL_BASE_DOMAIN and MIABI_EXTERNAL_BASE_PROVIDER to the default cluster. A
// set variable wins on every boot and locks its field; an empty one leaves the field to the console.
func (s *Service) PinExternalAccess(domain, provider string) error {
	s.externalDomainEnv = normalizeExternalDomain(domain)
	s.externalProviderEnv = strings.TrimSpace(provider)
	if s.store == nil || (s.externalDomainEnv == "" && s.externalProviderEnv == "") {
		return nil
	}
	def, err := s.store.FindDefault()
	if err != nil {
		return err
	}
	cols := map[string]any{}
	if d := s.externalDomainEnv; d != "" && d != def.ExternalBaseDomain {
		if err := s.checkExternalDomain(def.ID, d); err != nil {
			return fmt.Errorf("MIABI_EXTERNAL_BASE_DOMAIN: %w", err)
		}
		cols["external_base_domain"] = d
	}
	if p := s.externalProviderEnv; p != "" && p != def.ExternalCertProvider {
		if !certProviderPattern.MatchString(p) {
			return fmt.Errorf("MIABI_EXTERNAL_BASE_PROVIDER: %w", ErrInvalidCertProvider)
		}
		cols["external_cert_provider"] = p
	}
	if len(cols) == 0 {
		return nil
	}
	if err := s.store.UpdateColumns(def.ID, cols); err != nil {
		return err
	}
	s.externalChanged(def.ID)
	return nil
}

// externalColumns validates a patch's external access fields, returning only the columns that change.
func (s *Service) externalColumns(c *models.Cluster, p ClusterPatch) (map[string]any, error) {
	cols := map[string]any{}
	if p.ExternalBaseDomain != nil {
		if d := normalizeExternalDomain(*p.ExternalBaseDomain); d != c.ExternalBaseDomain {
			if c.IsDefault && s.externalDomainEnv != "" {
				return nil, ErrExternalAccessPinned
			}
			if err := s.checkExternalDomain(c.ID, d); err != nil {
				return nil, err
			}
			cols["external_base_domain"] = d
		}
	}
	if p.ExternalCertProvider != nil {
		if prov := strings.TrimSpace(*p.ExternalCertProvider); prov != c.ExternalCertProvider {
			if c.IsDefault && s.externalProviderEnv != "" {
				return nil, ErrExternalAccessPinned
			}
			if prov != "" && !certProviderPattern.MatchString(prov) {
				return nil, ErrInvalidCertProvider
			}
			cols["external_cert_provider"] = prov
		}
	}
	return cols, nil
}

// checkExternalDomain refuses a malformed domain, and one another cluster already uses: both would generate the
// same hostnames.
func (s *Service) checkExternalDomain(clusterID uint, domain string) error {
	if domain == "" {
		return nil
	}
	if len(domain) > 253 || !hostnamePattern.MatchString(domain) {
		return ErrInvalidExternalDomain
	}
	list, err := s.store.List()
	if err != nil {
		return err
	}
	for _, other := range list {
		if other.ID != clusterID && other.ExternalBaseDomain == domain {
			return ErrExternalDomainTaken
		}
	}
	return nil
}

func (s *Service) externalChanged(clusterID uint) {
	if s.externalListener != nil {
		go s.externalListener(context.Background(), clusterID)
	}
}

func (s *Service) markExternalPins(list []models.Cluster) {
	for i := range list {
		if list[i].IsDefault {
			list[i].ExternalDomainPinned = s.externalDomainEnv != ""
			list[i].ExternalProviderPinned = s.externalProviderEnv != ""
		}
	}
}

// normalizeExternalDomain accepts "*.apps.example.com" and "apps.example.com." for "apps.example.com".
func normalizeExternalDomain(raw string) string {
	d := strings.ToLower(strings.TrimSpace(raw))
	d = strings.TrimPrefix(d, "*.")
	return strings.TrimSuffix(strings.TrimPrefix(d, "."), ".")
}

// ExternalCertificates reads the certificates kept in the system workspace. Implemented by
// services/certificate.
type ExternalCertificates interface {
	SystemCertificate(id uint) (*models.Certificate, error)
	ResolveSystem(id uint) (certPEM, keyPEM string, err error)
}

// SetExternalCertificates wires the certificates a cluster may serve its generated URLs from.
func (s *Service) SetExternalCertificates(c ExternalCertificates) { s.externalCerts = c }

// externalCertColumns validates a patch's certificate choice against the domain the cluster will have after
// the patch. A domain change that leaves the current certificate behind is refused rather than silently
// falling back to a certificate per app.
func (s *Service) externalCertColumns(c *models.Cluster, p ClusterPatch) (map[string]any, error) {
	domain := c.ExternalBaseDomain
	if p.ExternalBaseDomain != nil {
		domain = normalizeExternalDomain(*p.ExternalBaseDomain)
	}
	certID := c.ExternalCertificateID
	if p.ExternalCertificateID != nil {
		certID = nil
		if *p.ExternalCertificateID != 0 {
			certID = p.ExternalCertificateID
		}
	}
	certChanged := !sameID(certID, c.ExternalCertificateID)
	if certID != nil && (certChanged || domain != c.ExternalBaseDomain) {
		if s.externalCerts == nil {
			return nil, ErrExternalCertNotFound
		}
		cert, err := s.externalCerts.SystemCertificate(*certID)
		if err != nil {
			return nil, ErrExternalCertNotFound
		}
		if err := externalCertProblem(cert, domain, time.Now()); err != nil {
			return nil, err
		}
	}
	if !certChanged {
		return nil, nil
	}
	return map[string]any{"external_certificate_id": certID}, nil
}

// externalCertProblem reports why a certificate cannot serve the generated URLs under domain. Those are
// <label>.<domain> or <label>-<port>.<domain>, one label deep, so only the domain's wildcard covers them all.
func externalCertProblem(cert *models.Certificate, domain string, now time.Time) error {
	if domain == "" {
		return ErrExternalCertNeedsDomain
	}
	if cert.Status != models.CertStatusActive || !now.Before(cert.NotAfter) {
		return ErrExternalCertUnusable
	}
	want := "*." + domain
	for _, n := range cert.DNSNames {
		if strings.EqualFold(strings.TrimSpace(n), want) {
			return nil
		}
	}
	return fmt.Errorf("%w (*.%s)", ErrExternalCertMismatch, domain)
}

// ExternalCertProblem reports why a certificate cannot serve the generated URLs under domain, nil when it can.
func ExternalCertProblem(cert *models.Certificate, domain string) error {
	return externalCertProblem(cert, domain, time.Now())
}

// ExternalCertificatePair returns the certificate a cluster serves its generated URLs from. ok is false when
// it has none or the one chosen can no longer serve them, and the gateway then falls back to a certificate
// per app rather than presenting an expired or mismatched one.
func (s *Service) ExternalCertificatePair(clusterID uint) (certPEM, keyPEM string, ok bool) {
	if s.store == nil || s.externalCerts == nil {
		return "", "", false
	}
	c, err := s.store.FindByID(clusterID)
	if err != nil || c.ExternalCertificateID == nil {
		return "", "", false
	}
	cert, err := s.externalCerts.SystemCertificate(*c.ExternalCertificateID)
	if err != nil {
		logger.Warn("generated URLs fall back to a certificate per app: certificate missing", "cluster", c.ID)
		return "", "", false
	}
	if err := externalCertProblem(cert, c.ExternalBaseDomain, time.Now()); err != nil {
		logger.Warn("generated URLs fall back to a certificate per app", "cluster", c.ID, "certificate", cert.Name, "reason", err)
		return "", "", false
	}
	certPEM, keyPEM, err = s.externalCerts.ResolveSystem(cert.ID)
	if err != nil || certPEM == "" {
		return "", "", false
	}
	return certPEM, keyPEM, true
}

func (s *Service) describeExternalCerts(list []models.Cluster) {
	if s.externalCerts == nil {
		return
	}
	now := time.Now()
	for i := range list {
		id := list[i].ExternalCertificateID
		if id == nil {
			continue
		}
		cert, err := s.externalCerts.SystemCertificate(*id)
		if err != nil {
			list[i].ExternalCertificate = &models.ExternalCertificateInfo{Problem: "the certificate no longer exists"}
			continue
		}
		info := &models.ExternalCertificateInfo{Name: cert.Name, DNSNames: cert.DNSNames, NotAfter: cert.NotAfter, Usable: true}
		if err := externalCertProblem(cert, list[i].ExternalBaseDomain, now); err != nil {
			info.Usable, info.Problem = false, err.Error()
		}
		list[i].ExternalCertificate = info
	}
}

func sameID(a, b *uint) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
