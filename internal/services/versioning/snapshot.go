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

package versioning

import (
	"encoding/json"
	"errors"

	"github.com/miabi-io/miabi/internal/models"
)

// Snapshot is a policy as a workspace bundle carries it: the rules, and the highest published version of
// each sequence, which is all a restore needs to continue numbering where the source left off.
type Snapshot struct {
	Settings  Settings          `json:"settings"`
	Managed   bool              `json:"managed,omitempty"`
	Published []SnapshotVersion `json:"published,omitempty"`
}

// SnapshotVersion is one restored sequence head.
type SnapshotVersion struct {
	Version string `json:"version"`
	Tag     string `json:"tag"`
	Series  string `json:"series,omitempty"`
}

// Export captures a pipeline's policy, or returns nil when its repository has none.
func (s *Service) Export(def *models.PipelineDefinition) (json.RawMessage, error) {
	p, err := s.repo.FindPolicyFor(owner(def))
	if err != nil {
		return nil, nil
	}
	existing, err := s.repo.Allocations(p.ID)
	if err != nil {
		return nil, err
	}
	snap := Snapshot{Settings: SettingsOf(p), Managed: p.ManagedBy == def.Name}
	heads := map[string]*models.VersionAllocation{}
	for i := range existing {
		a := &existing[i]
		if a.Status != models.AllocationPublished || a.Prerelease {
			continue
		}
		key := groupKey(a)
		if cur := heads[key]; cur == nil || compareVersions(a.Version, cur.Version) > 0 {
			heads[key] = a
		}
	}
	for _, a := range heads {
		snap.Published = append(snap.Published, SnapshotVersion{Version: a.Version, Tag: a.Tag, Series: a.Series})
	}
	return json.Marshal(snap)
}

// Import applies a bundled policy to a restored pipeline's repository and records each sequence head as
// published, so the next run continues past it. Heads the target already has are left alone.
func (s *Service) Import(def *models.PipelineDefinition, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return err
	}
	p, err := s.EnsureForPipeline(def, &snap.Settings, snap.Managed)
	if err != nil {
		return err
	}
	if !snap.Managed && p.ManagedBy == "" {
		n, err := snap.Settings.Normalize()
		if err != nil {
			return err
		}
		n.apply(p)
		if err := s.repo.SavePolicy(p); err != nil {
			return err
		}
	}
	current := p.Current
	var errs []error
	for _, v := range snap.Published {
		a := models.VersionAllocation{
			PolicyID: p.ID, Version: v.Version, Tag: v.Tag, Series: v.Series,
			Status: models.AllocationPublished, Note: "restored from a workspace bundle",
		}
		if err := s.repo.SaveAllocation(&a); err != nil {
			continue // already present in the target
		}
		if compareVersions(v.Version, current) > 0 {
			current = v.Version
		}
	}
	if current != p.Current {
		p.Current = current
		errs = append(errs, s.repo.SavePolicy(p))
	}
	return errors.Join(errs...)
}
