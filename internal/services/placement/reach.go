// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package placement

import (
	"errors"
	"fmt"

	"github.com/miabi-io/miabi/internal/models"
)

var (
	// ErrCrossLocation refuses a private link between two locations.
	ErrCrossLocation = errors.New("private networks don't span locations")
	// ErrCrossNode refuses a private link between two nodes of a location that runs no swarm.
	ErrCrossNode = errors.New("private networks don't span nodes in a location without a swarm")
)

// Site is one end of a private link: an application, a database or a volume.
type Site struct {
	Kind      string
	Name      string
	ClusterID uint
	ServerID  uint
}

func (s Site) String() string {
	if s.Kind == "" {
		return s.Name
	}
	return s.Kind + " " + s.Name
}

// Reach refuses a link that cannot resolve at runtime. Cluster ids must be resolved (never 0); swarm
// reports whether a cluster's workspace networks span its nodes, and label names a cluster.
func Reach(a, b Site, swarm func(clusterID uint) bool, label func(clusterID uint) string) error {
	if label == nil {
		label = func(id uint) string { return fmt.Sprintf("location %d", id) }
	}
	if a.ClusterID != b.ClusterID {
		return fmt.Errorf("%s is in %s, %s is in %s: %w", a, label(a.ClusterID), b, label(b.ClusterID), ErrCrossLocation)
	}
	if a.ServerID != b.ServerID && (swarm == nil || !swarm(a.ClusterID)) {
		return fmt.Errorf("%s and %s run on different nodes of %s: %w", a, b, label(a.ClusterID), ErrCrossNode)
	}
	return nil
}

// Reach is the package Reach with cluster 0 resolved to the default cluster and locations named.
func (s *Service) Reach(a, b Site, swarm func(clusterID uint) bool) error {
	a.ClusterID, b.ClusterID = s.resolveID(a.ClusterID), s.resolveID(b.ClusterID)
	return Reach(a, b, swarm, s.label)
}

// LocationName is the name manifests use for a cluster's location; empty when the cluster is gone.
func (s *Service) LocationName(clusterID uint) string {
	if c, err := s.clusters.FindByID(clusterID); err == nil {
		return c.Name
	}
	return ""
}

func (s *Service) resolveID(id uint) uint {
	if id != models.DefaultClusterID {
		return id
	}
	if c, err := s.clusters.FindDefault(); err == nil {
		return c.ID
	}
	return id
}

func (s *Service) label(id uint) string {
	if c, err := s.clusters.FindByID(id); err == nil {
		return c.Label()
	}
	return fmt.Sprintf("location %d", id)
}
