// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package organization

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// clusterSet answers from a fixed set of clusters, so a test can move one between organizations
// the way dedicating it does.
type clusterSet struct{ clusters []models.Cluster }

func (c *clusterSet) FindByID(id uint) (*models.Cluster, error) {
	for i := range c.clusters {
		if c.clusters[i].ID == id {
			return &c.clusters[i], nil
		}
	}
	return nil, ErrNotFound
}

func (c *clusterSet) ListByOrganization(orgID uint) ([]models.Cluster, error) {
	var out []models.Cluster
	for i := range c.clusters {
		if c.clusters[i].OrganizationID != nil && *c.clusters[i].OrganizationID == orgID {
			out = append(out, c.clusters[i])
		}
	}
	return out, nil
}

func (c *clusterSet) CountByOrganization(orgID uint) (int64, error) {
	owned, _ := c.ListByOrganization(orgID)
	return int64(len(owned)), nil
}

func (c *clusterSet) ReleaseOrganization(uint) error { return nil }

func (c *clusterSet) dedicate(clusterID, orgID uint) {
	for i := range c.clusters {
		if c.clusters[i].ID != clusterID {
			continue
		}
		if orgID == 0 {
			c.clusters[i].OrganizationID = nil
			return
		}
		c.clusters[i].OrganizationID = &orgID
	}
}

func defaultOf(t *testing.T, s *Service, orgID uint) uint {
	t.Helper()
	org, err := s.Get(orgID)
	if err != nil {
		t.Fatalf("get organization: %v", err)
	}
	if org.DefaultClusterID == nil {
		return 0
	}
	return *org.DefaultClusterID
}

// alignService builds an organization with the given default, over two shared clusters (10, 11).
func alignService(t *testing.T, def *uint) (*Service, *clusterSet, uint) {
	t.Helper()
	s, _ := newOrgService(t)
	set := &clusterSet{clusters: []models.Cluster{
		{ID: 10, Name: "alpha"},
		{ID: 11, Name: "beta"},
	}}
	s.SetClusters(set)
	acme, err := s.Create(CreateInput{DisplayName: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	if def != nil {
		org, gerr := s.Get(acme.ID)
		if gerr != nil {
			t.Fatal(gerr)
		}
		org.DefaultClusterID = def
		if err := s.repo.Update(org); err != nil {
			t.Fatal(err)
		}
	}
	return s, set, acme.ID
}

func ptr(n uint) *uint { return &n }

// The reported bug: an organization dedicated a cluster kept a default location on shared hardware
// it may no longer place in, so every new workspace was seeded with a location placement skips.
func TestAlignDefaultCluster_MovesOffSharedHardware(t *testing.T) {
	s, set, acme := alignService(t, ptr(11))

	set.dedicate(10, acme)
	if err := s.AlignDefaultCluster(acme, 10); err != nil {
		t.Fatalf("align: %v", err)
	}
	if got := defaultOf(t, s, acme); got != 10 {
		t.Fatalf("default = %d, want the newly dedicated cluster 10", got)
	}
}

// An organization with no default at all gets the cluster it was just given, rather than staying on
// the platform default it can no longer use.
func TestAlignDefaultCluster_FillsAnEmptyDefault(t *testing.T) {
	s, set, acme := alignService(t, nil)

	set.dedicate(10, acme)
	if err := s.AlignDefaultCluster(acme, 10); err != nil {
		t.Fatalf("align: %v", err)
	}
	if got := defaultOf(t, s, acme); got != 10 {
		t.Fatalf("default = %d, want 10", got)
	}
}

// A default that is already one of the organization's own is left alone: dedicating a second
// cluster must not silently move where its workspaces land.
func TestAlignDefaultCluster_KeepsAUsableDefault(t *testing.T) {
	s, set, acme := alignService(t, ptr(10))
	set.dedicate(10, acme)

	set.dedicate(11, acme)
	if err := s.AlignDefaultCluster(acme, 11); err != nil {
		t.Fatalf("align: %v", err)
	}
	if got := defaultOf(t, s, acme); got != 10 {
		t.Fatalf("default = %d, want the existing 10 left alone", got)
	}
}

// Releasing the cluster an organization defaulted to moves the default to one it still owns.
func TestAlignDefaultCluster_FollowsARelease(t *testing.T) {
	s, set, acme := alignService(t, ptr(10))
	set.dedicate(10, acme)
	set.dedicate(11, acme)

	set.dedicate(10, 0) // released back to shared
	if err := s.AlignDefaultCluster(acme, 0); err != nil {
		t.Fatalf("align: %v", err)
	}
	if got := defaultOf(t, s, acme); got != 11 {
		t.Fatalf("default = %d, want the still-owned 11", got)
	}
}

// An organization that owns nothing may use shared hardware again, so releasing its last cluster
// leaves the default where it is rather than clearing a location that is usable once more.
func TestAlignDefaultCluster_LastReleaseKeepsTheCluster(t *testing.T) {
	s, set, acme := alignService(t, ptr(10))
	set.dedicate(10, acme)

	set.dedicate(10, 0)
	if err := s.AlignDefaultCluster(acme, 0); err != nil {
		t.Fatalf("align: %v", err)
	}
	if got := defaultOf(t, s, acme); got != 10 {
		t.Fatalf("default = %d, want 10 kept now that it is shared again", got)
	}
}

// Without a cluster store wired there is nothing to align against, and no organization to break.
func TestAlignDefaultCluster_Unwired(t *testing.T) {
	s, _ := newOrgService(t)
	acme, err := s.Create(CreateInput{DisplayName: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AlignDefaultCluster(acme.ID, 10); err != nil {
		t.Fatalf("unwired align must be a no-op, got %v", err)
	}
	if got := defaultOf(t, s, acme.ID); got != 0 {
		t.Fatalf("default = %d, want none", got)
	}
}
