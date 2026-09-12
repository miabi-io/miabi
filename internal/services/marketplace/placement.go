// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package marketplace

import (
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/marketplace/manifest"
	"github.com/miabi-io/miabi/internal/services/placement"
)

// Placer resolves where an install lands. Satisfied by *placement.Service.
type Placer interface {
	Place(req placement.Request) (placement.Result, error)
	ResolveLocation(workspaceID uint, location string, admin bool) (*models.Cluster, error)
}

// SetPlacer wires location placement for installs (nil-safe; nil lands installs on the local node).
func (s *Service) SetPlacer(p Placer) { s.placer = p }

// installTarget decides where every resource of an install lands: in its location, and on the node of a
// database instance it reuses there, since an app shares a Docker network with its databases.
func (s *Service) installTarget(workspaceID uint, m *manifest.Manifest, in InstallInput) (placement.Result, error) {
	if s.placer == nil {
		node, err := s.installNode(workspaceID, m, in, 0)
		return placement.Result{ServerID: node}, err
	}
	loc, err := s.placer.ResolveLocation(workspaceID, in.Location, in.Admin)
	if err != nil {
		return placement.Result{}, err
	}
	colocate, err := s.installNode(workspaceID, m, in, loc.ID)
	if err != nil {
		return placement.Result{}, err
	}
	return s.placer.Place(placement.Request{WorkspaceID: workspaceID, Location: loc.Name, Colocate: colocate, Admin: in.Admin})
}

// LocationDatabases lists the database instances an install into location may reuse or pin: only those in
// that location, as private networks don't span locations. An empty location is the workspace default.
func (s *Service) LocationDatabases(workspaceID uint, location string, admin bool) ([]models.DatabaseInstance, error) {
	list, err := s.dbs.List(workspaceID)
	if err != nil || s.placer == nil {
		return list, err
	}
	loc, err := s.placer.ResolveLocation(workspaceID, location, admin)
	if err != nil {
		return nil, err
	}
	out := make([]models.DatabaseInstance, 0, len(list))
	for i := range list {
		if list[i].ClusterID == loc.ID {
			out = append(out, list[i])
		}
	}
	return out, nil
}
