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

package application

import (
	"github.com/miabi-io/miabi/internal/managedenv"
	"github.com/miabi-io/miabi/internal/models"
)

func (s *Service) SetManagedEnv(src *managedenv.Source) { s.managedEnv = src }

type ManagedVar struct {
	managedenv.Var
	// ReplacedBy is "app" or "stack" when a user variable of the same name wins.
	ReplacedBy string `json:"replaced_by,omitempty"`
}

type ManagedEnvPreview struct {
	Enabled bool `json:"enabled"`
	// Names is the whole contract, so a user variable can be flagged even when Miabi has no value for it.
	Names     []string     `json:"names"`
	Variables []ManagedVar `json:"variables"`
}

// ManagedEnvPreview shows what the next deploy would set, with the running release; deployment, digest and commit are only known then.
func (s *Service) ManagedEnvPreview(app *models.Application) ManagedEnvPreview {
	out := ManagedEnvPreview{Enabled: !app.MiabiEnvDisabled, Names: managedenv.Names, Variables: []ManagedVar{}}
	if app.MiabiEnvDisabled {
		return out
	}
	in := s.managedEnv.ForApp(app)
	// The running release, matching the Releases tab; the next deploy's number isn't known until it succeeds.
	if s.releases != nil {
		if rel, err := s.releases.FindActive(app.ID); err == nil {
			in.Release = rel.Version
		}
	}
	if app.SourceType != models.AppSourceGit {
		in.Image = app.ImageRef("")
	}
	own := map[string]bool{}
	for _, ev := range app.EnvVars {
		own[ev.Key] = true
	}
	stack := map[string]bool{}
	if app.StackID != nil && s.stackEnv != nil {
		if shared, err := s.stackEnv.ListByStack(*app.StackID); err == nil {
			for _, ev := range shared {
				stack[ev.Key] = true
			}
		}
	}
	for _, v := range managedenv.Build(in) {
		mv := ManagedVar{Var: v}
		switch {
		case own[v.Name]:
			mv.ReplacedBy = "app"
		case stack[v.Name]:
			mv.ReplacedBy = "stack"
		}
		out.Variables = append(out.Variables, mv)
	}
	return out
}
