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

// Package certificate manages workspace-scoped imported TLS certificates; ACME is handled by Goma. A
// certificate is parsed and validated on import (the key must match the leaf), its SANs recorded for
// host auto-matching, and its private key encrypted at rest. Routes reference one by id.
package certificate

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/quota"
	"github.com/miabi-io/miabi/internal/slug"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

var (
	ErrNotFound     = errors.New("certificate not found")
	ErrNameRequired = errors.New("certificate name is required")
	ErrNameTaken    = errors.New("a certificate with this name already exists")
	ErrPEMRequired  = errors.New("a certificate and private key are required")
	ErrInvalidPEM   = errors.New("invalid certificate or key (the key must match the certificate)")
	ErrInUse        = errors.New("certificate is referenced by one or more routes; detach them first")
	// ErrNoDomains blocks importing a certificate before any domain is registered:
	// a custom cert is meaningless without a domain to serve it on, and the import
	// must be tied to a domain the workspace controls.
	ErrNoDomains = errors.New("add a domain to this workspace before importing a certificate")
	// ErrDomainMismatch blocks a certificate whose CN/SANs are not all covered by
	// the workspace's registered domains — preventing importing a cert for hosts
	// the workspace does not control.
	ErrDomainMismatch = errors.New("certificate name is not covered by a registered domain")
	// ErrDomainUnverified refuses a certificate for a domain the workspace has registered but not
	// proven it controls. Registering a name proves nothing, and a certificate for a name someone
	// else serves takes that hostname's TLS.
	ErrDomainUnverified = errors.New("certificate name needs a verified domain")
	// ErrDomainBanned refuses a certificate under a domain an admin has banned.
	ErrDomainBanned = errors.New("certificate name is under a banned domain")
	// ErrServesExternalAccess refuses deleting a certificate a cluster serves its generated URLs from.
	ErrServesExternalAccess = errors.New("certificate serves generated app URLs; choose another certificate for the location first")
)

// SystemWorkspace finds the platform's system workspace, whose certificates may serve generated app URLs.
type SystemWorkspace interface {
	FindSystem() (*models.Workspace, error)
}

// RouteRefs reports the routes referencing a certificate (delete-guard + usage).
// Implemented by the route service; injected after construction.
type RouteRefs interface {
	RoutesUsingCertificate(workspaceID, certID uint) ([]models.Route, error)
}

// DomainLister lists a workspace's registered domains, used to gate certificate
// imports to hosts the workspace controls. Injected after construction; nil
// disables the domain checks (back-compat / tests).
type DomainLister interface {
	ListByWorkspace(workspaceID uint) ([]models.Domain, error)
}

// WorkspacePrivilege reports whether a workspace holds the platform-admin-granted privileged flag.
type WorkspacePrivilege interface {
	FindByID(id uint) (*models.Workspace, error)
}

type Service struct {
	repo       *repositories.CertificateRepository
	routes     RouteRefs
	domains    DomainLister
	workspaces WorkspacePrivilege
	quota      *quota.Service
	system     SystemWorkspace
	// clusterRefs names the clusters serving generated URLs from a certificate; onChange is told when a
	// certificate's material is replaced or renewed.
	clusterRefs func(certID uint) ([]string, error)
	onChange    func(*models.Certificate)
}

func NewService(repo *repositories.CertificateRepository) *Service {
	return &Service{repo: repo}
}

// SetRouteRefs wires the route-usage lookup used for the delete guard and usage.
func (s *Service) SetRouteRefs(r RouteRefs) { s.routes = r }

// SetDomains wires the registered-domain lister used to gate certificate imports.
func (s *Service) SetDomains(d DomainLister) { s.domains = d }

// SetWorkspacePrivilege wires the privileged-workspace check that mirrors the route serve gate.
func (s *Service) SetWorkspacePrivilege(w WorkspacePrivilege) { s.workspaces = w }

// SetQuota wires the plan/quota enforcer (nil-safe; gates the custom-TLS capability).
func (s *Service) SetQuota(q *quota.Service) { s.quota = q }

// SetSystemWorkspace wires the lookup behind SystemCertificate and ResolveSystem.
func (s *Service) SetSystemWorkspace(w SystemWorkspace) { s.system = w }

// SetClusterRefs wires the delete guard for certificates serving a cluster's generated URLs.
func (s *Service) SetClusterRefs(fn func(certID uint) ([]string, error)) { s.clusterRefs = fn }

// SetOnChange wires a callback run after a certificate is replaced or renewed.
func (s *Service) SetOnChange(fn func(*models.Certificate)) { s.onChange = fn }

func (s *Service) changed(c *models.Certificate) {
	if s.onChange != nil {
		s.onChange(c)
	}
}

func (s *Service) systemID() (uint, error) {
	if s.system == nil {
		return 0, ErrNotFound
	}
	ws, err := s.system.FindSystem()
	if err != nil {
		return 0, ErrNotFound
	}
	return ws.ID, nil
}

// SystemCertificate returns a certificate kept in the system workspace.
func (s *Service) SystemCertificate(id uint) (*models.Certificate, error) {
	wsID, err := s.systemID()
	if err != nil {
		return nil, err
	}
	return s.Get(wsID, id)
}

// ListSystem returns the system workspace's certificates.
func (s *Service) ListSystem() ([]models.Certificate, error) {
	wsID, err := s.systemID()
	if err != nil {
		return nil, err
	}
	return s.List(wsID)
}

// ResolveSystem returns a system-workspace certificate's PEM and decrypted key.
func (s *Service) ResolveSystem(id uint) (certPEM, keyPEM string, err error) {
	wsID, err := s.systemID()
	if err != nil {
		return "", "", err
	}
	return s.Resolve(wsID, id)
}

func (s *Service) List(workspaceID uint) ([]models.Certificate, error) {
	return s.repo.ListByWorkspace(workspaceID)
}

func (s *Service) Get(workspaceID, id uint) (*models.Certificate, error) {
	c, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return c, nil
}

// Import parses and validates a certificate and key, encrypts the key, and stores it with its parsed
// metadata. name is the desired unique slug handle, normalized to canonical form; displayName is the
// free-text label, falling back to the raw name when blank.
func (s *Service) Import(workspaceID uint, name, displayName, certPEM, keyPEM string) (*models.Certificate, error) {
	if err := s.quota.Require(workspaceID, quota.CapCustomTLS); err != nil {
		return nil, err
	}
	handle := slug.Make(name, "")
	if handle == "" {
		return nil, ErrNameRequired
	}
	label := strings.TrimSpace(displayName)
	if label == "" {
		label = strings.TrimSpace(name)
	}
	taken, err := s.repo.ExistsByName(workspaceID, handle)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, ErrNameTaken
	}
	meta, err := parse(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	if err := s.validateAgainstDomains(workspaceID, meta); err != nil {
		return nil, err
	}
	keyEnc, err := crypto.EncryptWS(workspaceID, keyPEM)
	if err != nil {
		return nil, err
	}
	cert := &models.Certificate{
		WorkspaceID: workspaceID, Name: handle, DisplayName: label,
		CertPEM: strings.TrimSpace(certPEM), KeyEnc: keyEnc,
	}
	meta.apply(cert)
	if err := s.repo.Create(cert); err != nil {
		return nil, err
	}
	return cert, nil
}

// Replace swaps the cert/key of an existing certificate (renewal), re-parsing
// metadata. A blank name keeps the current name.
func (s *Service) Replace(workspaceID, id uint, name, certPEM, keyPEM string) (*models.Certificate, error) {
	cert, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	meta, err := parse(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	if err := s.validateAgainstDomains(workspaceID, meta); err != nil {
		return nil, err
	}
	keyEnc, err := crypto.EncryptWS(workspaceID, keyPEM)
	if err != nil {
		return nil, err
	}
	if n := strings.TrimSpace(name); n != "" {
		cert.Name = n
	}
	cert.CertPEM = strings.TrimSpace(certPEM)
	cert.KeyEnc = keyEnc
	meta.apply(cert)
	if err := s.repo.Update(cert); err != nil {
		return nil, err
	}
	s.changed(cert)
	return cert, nil
}

// BeginManaged creates (or resets) a managed ACME certificate row in the
// "issuing" state, ready for the issuer to fill in. Reuses an existing ACME row
// of the same name (re-issue / renew); refuses to overwrite an imported cert.
func (s *Service) BeginManaged(workspaceID uint, name string, dnsProviderID uint, autoRenew bool) (*models.Certificate, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrNameRequired
	}
	pid := dnsProviderID
	if existing, err := s.repo.FindByName(workspaceID, name); err == nil {
		if existing.Source != models.CertSourceACME {
			return nil, ErrNameTaken // don't clobber an imported cert
		}
		existing.Status = models.CertStatusIssuing
		existing.LastError = ""
		existing.DNSProviderID = &pid
		existing.AutoRenew = autoRenew
		if err := s.repo.Update(existing); err != nil {
			return nil, err
		}
		return existing, nil
	}
	cert := &models.Certificate{
		WorkspaceID: workspaceID, Name: name,
		Source: models.CertSourceACME, Status: models.CertStatusIssuing,
		DNSProviderID: &pid, AutoRenew: autoRenew,
	}
	if err := s.repo.Create(cert); err != nil {
		return nil, err
	}
	return cert, nil
}

// CompleteManaged stores an issued cert/key on a managed row and marks it active.
func (s *Service) CompleteManaged(workspaceID, id uint, certPEM, keyPEM string) (*models.Certificate, error) {
	cert, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return nil, ErrNotFound
	}
	meta, err := parse(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	keyEnc, err := crypto.EncryptWS(workspaceID, keyPEM)
	if err != nil {
		return nil, err
	}
	cert.CertPEM = strings.TrimSpace(certPEM)
	cert.KeyEnc = keyEnc
	cert.Source = models.CertSourceACME
	cert.Status = models.CertStatusActive
	cert.LastError = ""
	meta.apply(cert)
	if err := s.repo.Update(cert); err != nil {
		return nil, err
	}
	s.changed(cert)
	return cert, nil
}

// FailManaged marks a managed row failed with the cause (kept for the UI).
func (s *Service) FailManaged(workspaceID, id uint, cause string) error {
	cert, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	cert.Status = models.CertStatusFailed
	cert.LastError = cause
	return s.repo.Update(cert)
}

// ListManagedDue returns ACME auto-renew certificates within `within` of expiry.
func (s *Service) ListManagedDue(within time.Duration) ([]models.Certificate, error) {
	return s.repo.ListManagedExpiring(time.Now().Add(within))
}

// Delete removes a certificate, refusing while any route references it.
func (s *Service) Delete(workspaceID, id uint) error {
	cert, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return ErrNotFound
	}
	if used, err := s.Usage(workspaceID, id); err != nil {
		return err
	} else if len(used) > 0 {
		return ErrInUse
	}
	if s.clusterRefs != nil {
		names, err := s.clusterRefs(cert.ID)
		if err != nil {
			return err
		}
		if len(names) > 0 {
			return fmt.Errorf("%w: %s", ErrServesExternalAccess, strings.Join(names, ", "))
		}
	}
	return s.repo.Delete(cert.ID)
}

// Usage returns the routes referencing a certificate.
func (s *Service) Usage(workspaceID, id uint) ([]models.Route, error) {
	if s.routes == nil {
		return nil, nil
	}
	return s.routes.RoutesUsingCertificate(workspaceID, id)
}

// MatchHost returns the certificates whose SANs cover the given host (exact or
// wildcard), most-recently-expiring last — for route-form auto-select.
func (s *Service) MatchHost(workspaceID uint, host string) ([]models.Certificate, error) {
	all, err := s.repo.ListByWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}
	host = strings.ToLower(strings.TrimSpace(host))
	out := make([]models.Certificate, 0)
	for i := range all {
		if hostMatches(all[i].DNSNames, host) {
			out = append(out, all[i])
		}
	}
	return out, nil
}

const expiryHorizon = 30 * 24 * time.Hour

// CheckExpiry scans all certificates expiring within the horizon and logs a
// warning for each (with days remaining). Run on a daily schedule by the cron
// manager; returns the number flagged.
func (s *Service) CheckExpiry() (int, error) {
	now := time.Now()
	certs, err := s.repo.ListExpiringBefore(now.Add(expiryHorizon))
	if err != nil {
		return 0, err
	}
	for i := range certs {
		days := int(certs[i].NotAfter.Sub(now).Hours() / 24)
		logger.Warn("TLS certificate expiring soon",
			"name", certs[i].Name, "workspace", certs[i].WorkspaceID,
			"days_left", days, "not_after", certs[i].NotAfter.Format(time.RFC3339))
	}
	return len(certs), nil
}

// Resolve returns a certificate's PEM and decrypted key for proxy rendering.
func (s *Service) Resolve(workspaceID, id uint) (certPEM, keyPEM string, err error) {
	cert, err := s.repo.FindInWorkspace(workspaceID, id)
	if err != nil {
		return "", "", ErrNotFound
	}
	key, err := crypto.Decrypt(cert.KeyEnc)
	if err != nil {
		return "", "", err
	}
	return cert.CertPEM, key, nil
}

type certMeta struct {
	commonName string
	dnsNames   []string
	issuer     string
	notBefore  time.Time
	notAfter   time.Time
	serialHex  string
}

func (m *certMeta) apply(c *models.Certificate) {
	c.CommonName = m.commonName
	c.DNSNames = m.dnsNames
	c.Issuer = m.issuer
	c.NotBefore = m.notBefore
	c.NotAfter = m.notAfter
	c.SerialHex = m.serialHex
}

// parse validates that the key matches the leaf certificate and extracts the
// metadata Miabi tracks (CN, SANs, issuer, validity window, serial).
func parse(certPEM, keyPEM string) (*certMeta, error) {
	if strings.TrimSpace(certPEM) == "" || strings.TrimSpace(keyPEM) == "" {
		return nil, ErrPEMRequired
	}
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPEM, err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPEM, err)
	}
	names := leaf.DNSNames
	if len(names) == 0 && leaf.Subject.CommonName != "" {
		names = []string{leaf.Subject.CommonName}
	}
	return &certMeta{
		commonName: leaf.Subject.CommonName,
		dnsNames:   names,
		issuer:     leaf.Issuer.String(),
		notBefore:  leaf.NotBefore,
		notAfter:   leaf.NotAfter,
		serialHex:  leaf.SerialNumber.Text(16),
	}, nil
}

// validateAgainstDomains enforces that a certificate is importable only when the workspace has at least one
// registered domain, and every name the cert asserts (Common Name and SANs) falls under one of them. This
// ties an imported cert to hosts the workspace actually controls. A nil domain lister skips the checks.
func (s *Service) validateAgainstDomains(workspaceID uint, meta *certMeta) error {
	if s.domains == nil {
		return nil
	}
	domains, err := s.domains.ListByWorkspace(workspaceID)
	if err != nil {
		return err
	}
	if len(domains) == 0 {
		return ErrNoDomains
	}
	// Collect the cert's asserted names (CN + SANs), normalized and de-duplicated.
	seen := map[string]bool{}
	names := make([]string, 0, len(meta.dnsNames)+1)
	for _, n := range append([]string{meta.commonName}, meta.dnsNames...) {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		names = append(names, n)
	}

	privileged := s.privileged(workspaceID)
	for _, n := range names {
		d := coveringDomain(n, domains)
		switch {
		case d == nil:
			return fmt.Errorf("%w: %s", ErrDomainMismatch, n)
		case d.Banned:
			return fmt.Errorf("%w: %s", ErrDomainBanned, n)
		case !d.Verified && !privileged:
			return fmt.Errorf("%w: %s", ErrDomainUnverified, n)
		}
	}
	return nil
}

// privileged reports whether the workspace may act on an unverified domain, matching the route
// serve gate. Unwired or unreadable means not privileged.
func (s *Service) privileged(workspaceID uint) bool {
	if s.workspaces == nil {
		return false
	}
	ws, err := s.workspaces.FindByID(workspaceID)
	return err == nil && ws.Privileged
}

// coveringDomain returns the registered domain a certificate name (a host or a "*.x" wildcard) falls
// under — equal to it, a subdomain of it, or the domain's own wildcard — or nil when none does. The
// caller needs the domain itself, not just a yes/no, to check that it is verified and unbanned.
func coveringDomain(name string, domains []models.Domain) *models.Domain {
	for i := range domains {
		d := strings.ToLower(strings.TrimSpace(domains[i].Name))
		if d == "" {
			continue
		}
		// "*.example.com" or "app.example.com" or "example.com" all sit under "example.com".
		if name == d || strings.HasSuffix(name, "."+d) {
			return &domains[i]
		}
	}
	return nil
}

// hostMatches reports whether any SAN covers host, supporting a single-label
// wildcard (*.example.com matches app.example.com, not example.com or a.b.x).
func hostMatches(sans []string, host string) bool {
	if host == "" {
		return false
	}
	for _, san := range sans {
		san = strings.ToLower(strings.TrimSpace(san))
		if san == host {
			return true
		}
		if strings.HasPrefix(san, "*.") {
			suffix := san[1:] // ".example.com"
			if strings.HasSuffix(host, suffix) {
				// exactly one extra label before the suffix
				label := host[:len(host)-len(suffix)]
				if label != "" && !strings.Contains(label, ".") {
					return true
				}
			}
		}
	}
	return false
}
