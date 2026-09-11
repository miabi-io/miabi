// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package marketplace

import (
	"github.com/miabi-io/miabi/internal/services/marketplace/manifest"
	"github.com/miabi-io/miabi/internal/services/placement"
)

// Placer resolves where an install lands. Satisfied by *placement.Service.
type Placer interface {
	Place(req placement.Request) (placement.Result, error)
}

// SetPlacer wires location placement for installs (nil-safe; nil lands installs on the local node).
func (s *Service) SetPlacer(p Placer) { s.placer = p }

// installTarget decides where every resource of an install lands. A dependency reusing an existing database
// instance makes that instance's node authoritative; otherwise placement picks a node in the install's location.
func (s *Service) installTarget(workspaceID uint, m *manifest.Manifest, in InstallInput) (placement.Result, error) {
	colocate := s.installNode(workspaceID, m, in)
	if s.placer == nil {
		return placement.Result{ServerID: colocate}, nil
	}
	return s.placer.Place(placement.Request{WorkspaceID: workspaceID, Location: in.Location, Colocate: colocate, Admin: in.Admin})
}
