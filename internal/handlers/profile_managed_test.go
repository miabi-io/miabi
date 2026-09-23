// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"strings"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// The console disables the name/username fields from the profile payload, so an
// account with no stored auth source has to report a concrete one.
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

// The refusal has to name where the profile actually lives, or the user has nowhere
// to go with it.
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
