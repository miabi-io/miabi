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
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
)

func rehosted(s *Service) <-chan uint {
	changed := make(chan uint, 4)
	s.SetExternalAccessListener(func(_ context.Context, id uint) { changed <- id })
	return changed
}

func expectRehost(t *testing.T, changed <-chan uint, want uint) {
	t.Helper()
	select {
	case id := <-changed:
		if id != want {
			t.Errorf("re-hosted cluster %d, want %d", id, want)
		}
	case <-time.After(time.Second):
		t.Errorf("cluster %d was not re-hosted", want)
	}
}

func TestUpdateClusterSetsTheExternalDomain(t *testing.T) {
	store := &memStore{
		def:    models.Cluster{ID: 1, IsDefault: true, Name: "default"},
		others: map[uint]models.Cluster{2: {ID: 2, Name: "paris", ExternalBaseDomain: "apps.eu.example.com"}},
	}
	s := &Service{store: store}
	changed := rehosted(s)

	domain, provider := " *.Apps.Example.com. ", "letsencrypt"
	c, err := s.UpdateCluster(1, ClusterPatch{ExternalBaseDomain: &domain, ExternalCertProvider: &provider})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if c.ExternalBaseDomain != "apps.example.com" || c.ExternalCertProvider != "letsencrypt" {
		t.Errorf("cluster = %+v, want apps.example.com with letsencrypt", c)
	}
	expectRehost(t, changed, 1)

	if _, err := s.UpdateCluster(1, ClusterPatch{ExternalBaseDomain: &domain}); err != nil {
		t.Fatalf("saving the same domain: %v", err)
	}
	select {
	case id := <-changed:
		t.Errorf("cluster %d re-hosted although its domain did not change", id)
	case <-time.After(50 * time.Millisecond):
	}

	for raw, want := range map[string]error{
		"not a domain":        ErrInvalidExternalDomain,
		"localhost":           ErrInvalidExternalDomain,
		"apps.eu.example.com": ErrExternalDomainTaken,
	} {
		if _, err := s.UpdateCluster(1, ClusterPatch{ExternalBaseDomain: &raw}); !errors.Is(err, want) {
			t.Errorf("domain %q: err = %v, want %v", raw, err, want)
		}
	}
	bad := "lets encrypt"
	if _, err := s.UpdateCluster(1, ClusterPatch{ExternalCertProvider: &bad}); !errors.Is(err, ErrInvalidCertProvider) {
		t.Errorf("provider %q: err = %v, want ErrInvalidCertProvider", bad, err)
	}
	if got, _ := s.ExternalAccess(models.DefaultClusterID); got != "apps.example.com" {
		t.Errorf("ExternalAccess(default) = %q, want apps.example.com", got)
	}

	cleared := ""
	if c, err := s.UpdateCluster(2, ClusterPatch{ExternalBaseDomain: &cleared}); err != nil || c.ExternalBaseDomain != "" {
		t.Fatalf("clearing = %+v, %v; want external access off", c, err)
	}
	expectRehost(t, changed, 2)
}

func TestTheEnvironmentPinsTheDefaultClusterExternalAccess(t *testing.T) {
	store := &memStore{
		def:    models.Cluster{ID: 1, IsDefault: true, Name: "default", ExternalBaseDomain: "old.example.com"},
		others: map[uint]models.Cluster{2: {ID: 2, Name: "paris"}},
	}
	s := &Service{store: store}
	changed := rehosted(s)

	if err := s.PinExternalAccess("*.apps.example.com", ""); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if store.def.ExternalBaseDomain != "apps.example.com" {
		t.Errorf("default domain = %q, want the environment's", store.def.ExternalBaseDomain)
	}
	expectRehost(t, changed, 1)

	other := "apps.example.net"
	if _, err := s.UpdateCluster(1, ClusterPatch{ExternalBaseDomain: &other}); !errors.Is(err, ErrExternalAccessPinned) {
		t.Errorf("editing a pinned domain err = %v, want ErrExternalAccessPinned", err)
	}
	same, provider := "apps.example.com", "zerossl"
	c, err := s.UpdateCluster(1, ClusterPatch{ExternalBaseDomain: &same, ExternalCertProvider: &provider})
	if err != nil || c.ExternalCertProvider != "zerossl" {
		t.Errorf("unchanged pinned domain with a provider edit = %+v, %v; want the provider saved", c, err)
	}
	expectRehost(t, changed, 1)
	if _, err := s.UpdateCluster(2, ClusterPatch{ExternalBaseDomain: &same}); !errors.Is(err, ErrExternalDomainTaken) {
		t.Errorf("another cluster taking the pinned domain err = %v, want ErrExternalDomainTaken", err)
	}

	list, err := s.Clusters()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.ExternalDomainPinned != c.IsDefault || c.ExternalProviderPinned {
			t.Errorf("%s: domain pinned %v, provider pinned %v; want only the default cluster's domain pinned",
				c.Name, c.ExternalDomainPinned, c.ExternalProviderPinned)
		}
	}
}

type fakeCerts map[uint]*models.Certificate

func (f fakeCerts) SystemCertificate(id uint) (*models.Certificate, error) {
	if c, ok := f[id]; ok {
		return c, nil
	}
	return nil, errors.New("not found")
}

func (f fakeCerts) ResolveSystem(id uint) (string, string, error) {
	if _, ok := f[id]; !ok {
		return "", "", errors.New("not found")
	}
	return "CERT", "KEY", nil
}

func wildcard(id uint, names ...string) *models.Certificate {
	return &models.Certificate{ID: id, Name: "wild", DNSNames: names, Status: models.CertStatusActive, NotAfter: time.Now().Add(30 * 24 * time.Hour)}
}

func resynced(s *Service) <-chan uint {
	ch := make(chan uint, 4)
	s.SetGatewayListener(func(_ context.Context, id uint) { ch <- id })
	return ch
}

func TestExternalCertificateMustCoverTheWildcard(t *testing.T) {
	expired := wildcard(4, "*.apps.example.com")
	expired.NotAfter = time.Now().Add(-time.Hour)
	issuing := wildcard(5, "*.apps.example.com")
	issuing.Status = models.CertStatusIssuing
	store := &memStore{
		def:    models.Cluster{ID: 1, IsDefault: true, Name: "default", ExternalBaseDomain: "apps.example.com"},
		others: map[uint]models.Cluster{2: {ID: 2, Name: "bare"}},
	}
	s := &Service{store: store}
	s.SetExternalCertificates(fakeCerts{
		1: wildcard(1, "*.apps.example.com", "apps.example.com"),
		2: wildcard(2, "app.apps.example.com"),
		3: wildcard(3, "*.other.example.com"),
		4: expired, 5: issuing,
	})
	sync := resynced(s)

	one := uint(1)
	c, err := s.UpdateCluster(1, ClusterPatch{ExternalCertificateID: &one})
	if err != nil || c.ExternalCertificateID == nil || *c.ExternalCertificateID != 1 {
		t.Fatalf("set wildcard = %+v, %v", c.ExternalCertificateID, err)
	}
	if c.ExternalCertificate == nil || !c.ExternalCertificate.Usable {
		t.Errorf("certificate info = %+v, want usable", c.ExternalCertificate)
	}
	expectRehost(t, sync, 1)

	for id, want := range map[uint]error{
		2: ErrExternalCertMismatch, 3: ErrExternalCertMismatch,
		4: ErrExternalCertUnusable, 5: ErrExternalCertUnusable, 9: ErrExternalCertNotFound,
	} {
		if _, err := s.UpdateCluster(1, ClusterPatch{ExternalCertificateID: &id}); !errors.Is(err, want) {
			t.Errorf("certificate %d: err = %v, want %v", id, err, want)
		}
	}
	if _, err := s.UpdateCluster(2, ClusterPatch{ExternalCertificateID: &one}); !errors.Is(err, ErrExternalCertNeedsDomain) {
		t.Errorf("no external domain: err = %v, want ErrExternalCertNeedsDomain", err)
	}
}

// Moving the domain out from under the certificate is refused; moving both together is fine.
func TestExternalDomainChangeKeepsTheCertificateHonest(t *testing.T) {
	one := uint(1)
	store := &memStore{def: models.Cluster{ID: 1, IsDefault: true, Name: "default", ExternalBaseDomain: "apps.example.com", ExternalCertificateID: &one}}
	s := &Service{store: store}
	s.SetExternalCertificates(fakeCerts{1: wildcard(1, "*.apps.example.com"), 2: wildcard(2, "*.apps.new.example.com")})
	_ = rehosted(s)

	moved := "apps.new.example.com"
	if _, err := s.UpdateCluster(1, ClusterPatch{ExternalBaseDomain: &moved}); !errors.Is(err, ErrExternalCertMismatch) {
		t.Fatalf("domain change past the certificate: err = %v, want ErrExternalCertMismatch", err)
	}
	two := uint(2)
	if _, err := s.UpdateCluster(1, ClusterPatch{ExternalBaseDomain: &moved, ExternalCertificateID: &two}); err != nil {
		t.Fatalf("domain and certificate together: %v", err)
	}
	zero := uint(0)
	c, err := s.UpdateCluster(1, ClusterPatch{ExternalCertificateID: &zero})
	if err != nil || c.ExternalCertificateID != nil || c.ExternalCertificate != nil {
		t.Fatalf("clearing = %+v, %v; want a certificate per app", c.ExternalCertificateID, err)
	}
}

// An unusable certificate is never served: the gateway falls back to a certificate per app.
func TestExternalCertificatePairFallsBack(t *testing.T) {
	one := uint(1)
	expired := wildcard(1, "*.apps.example.com")
	store := &memStore{def: models.Cluster{ID: 1, IsDefault: true, Name: "default", ExternalBaseDomain: "apps.example.com", ExternalCertificateID: &one}}
	s := &Service{store: store}
	s.SetExternalCertificates(fakeCerts{1: expired})
	if cert, key, ok := s.ExternalCertificatePair(1); !ok || cert != "CERT" || key != "KEY" {
		t.Fatalf("usable wildcard: got %q %q %v", cert, key, ok)
	}
	expired.NotAfter = time.Now().Add(-time.Minute)
	if _, _, ok := s.ExternalCertificatePair(1); ok {
		t.Error("an expired wildcard must not be served")
	}
	store.def.ExternalBaseDomain = "apps.moved.example.com"
	expired.NotAfter = time.Now().Add(time.Hour)
	if _, _, ok := s.ExternalCertificatePair(1); ok {
		t.Error("a wildcard for another domain must not be served")
	}
}
