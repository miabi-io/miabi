// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
