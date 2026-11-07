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

package handlers

import (
	"errors"
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestSystemPlanEdit(t *testing.T) {
	system := &models.Plan{Name: models.UnlimitedPlanName, System: true}
	ordinary := &models.Plan{Name: "Pro"}

	cases := []struct {
		name        string
		plan        *models.Plan
		newName     string
		makeDefault bool
		wantRefusal error
	}{
		// pinUnlimitedPlan resolves it by name and fails soft, so a rename silently drops it to default limits.
		{"renaming the system plan", system, "Platform", false, errSystemPlanRename},
		{"making it the default", system, "", true, errSystemPlanDefault},
		{"both at once reports the rename", system, "Platform", true, errSystemPlanRename},

		// What stays allowed.
		{"editing its limits", system, "", false, nil},
		{"submitting its own name unchanged", system, models.UnlimitedPlanName, false, nil},

		// An ordinary plan is untouched by any of it.
		{"renaming an ordinary plan", ordinary, "Professional", false, nil},
		{"defaulting an ordinary plan", ordinary, "", true, nil},
		{"a nil plan", nil, "anything", true, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := systemPlanEdit(c.plan, c.newName, c.makeDefault)
			if c.wantRefusal == nil {
				if err != nil {
					t.Errorf("got %v, want it allowed", err)
				}
				return
			}
			if !errors.Is(err, c.wantRefusal) {
				t.Errorf("got %v, want %v", err, c.wantRefusal)
			}
		})
	}
}

func TestSystemPlanRefusalsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, err := range []error{errSystemPlanDelete, errSystemPlanRename, errSystemPlanDefault, errSystemPlanAssign} {
		msg := err.Error()
		if seen[msg] {
			t.Errorf("duplicate refusal message: %q", msg)
		}
		seen[msg] = true
		if len(msg) < 40 {
			t.Errorf("refusal %q is too terse to act on", msg)
		}
	}
}

func TestSystemPlanIsUnreachableByATenant(t *testing.T) {
	routes := map[string]error{
		"deleting it":                 errSystemPlanDelete,
		"renaming it":                 errSystemPlanRename,
		"making it the default":       errSystemPlanDefault,
		"assigning it to a workspace": errSystemPlanAssign,
	}
	for route, err := range routes {
		if err == nil {
			t.Errorf("%s has no refusal", route)
			continue
		}
		// A refusal that only says no leaves the reader guessing whether it is a bug or a rule.
		msg := err.Error()
		if !strings.Contains(msg, "cannot") || !strings.Contains(msg, ";") {
			t.Errorf("refusal for %s does not say what is refused and why: %q", route, msg)
		}
	}
}
