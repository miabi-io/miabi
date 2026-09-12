// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package cluster

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
)

var (
	// ErrInvalidExternalDomain is returned for an external domain that is not a DNS name.
	ErrInvalidExternalDomain = errors.New("the external domain must be a DNS name such as apps.eu-central.example.com")
	// ErrExternalDomainTaken is returned when another cluster already generates URLs under the domain.
	ErrExternalDomainTaken = errors.New("another cluster already uses this external domain")
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
