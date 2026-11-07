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
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// The console disables profile fields from this payload, so a missing auth source must report a concrete one.
func TestProfileOf_ReportsTheAuthSource(t *testing.T) {
	for _, tc := range []struct {
		name        string
		source      string
		wantSource  string
		wantManaged bool
	}{
		{"legacy blank reads as local", "", models.AuthSourceLocal, false},
		{"local", models.AuthSourceLocal, models.AuthSourceLocal, false},
		{"ldap", models.AuthSourceLDAP, models.AuthSourceLDAP, true},
		{"oauth", models.AuthSourceOAuth, models.AuthSourceOAuth, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := profileOf(&models.User{ID: 1, Name: "A", Email: "a@b.test", AuthSource: tc.source})
			if p.AuthSource != tc.wantSource {
				t.Errorf("auth_source = %q, want %q", p.AuthSource, tc.wantSource)
			}
			if p.ProfileManaged != tc.wantManaged {
				t.Errorf("profile_managed = %v, want %v", p.ProfileManaged, tc.wantManaged)
			}
		})
	}
}

// The refusal must name where the profile actually lives.
func TestAuthSourceLabel(t *testing.T) {
	for _, src := range []string{models.AuthSourceLDAP, models.AuthSourceOAuth, models.AuthSourceSAML, models.AuthSourceSCIM, "something-new"} {
		if label := authSourceLabel(src); strings.TrimSpace(label) == "" {
			t.Errorf("authSourceLabel(%q) is empty", src)
		}
	}
	if got := authSourceLabel("LDAP"); got != authSourceLabel(models.AuthSourceLDAP) {
		t.Errorf("label is case sensitive: %q", got)
	}
}
