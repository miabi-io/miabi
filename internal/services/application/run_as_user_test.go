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
	"errors"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/quota"
)

func TestValidRunAsUser(t *testing.T) {
	// Enforcement off and no server mandate: the workspace may pick any account.
	free := &Service{quota: &quota.Service{}}
	for _, v := range []string{"", "1000", "1000:1000", "node", "root", "0"} {
		got, err := free.validRunAsUser(1, v, false)
		if err != nil {
			t.Errorf("%q should be accepted with no mandate: %v", v, err)
		}
		if got != v {
			t.Errorf("validRunAsUser(%q) = %q, want it stored as given", v, got)
		}
	}

	// A malformed value is rejected regardless of profile.
	if _, err := free.validRunAsUser(1, "not a user", false); !errors.Is(err, models.ErrRunAsUserInvalid) {
		t.Errorf("expected ErrRunAsUserInvalid, got %v", err)
	}

	// A name is refused too: the image's own /etc/passwd decides what it maps to.
	forcedQuota := &quota.Service{}
	forcedQuota.SetForceNonRoot(true) // the server-level mandate, independent of any plan
	forced := &Service{quota: forcedQuota}
	for _, v := range []string{"0", "0:0", "root", "node", "appuser"} {
		if _, err := forced.validRunAsUser(1, v, false); !errors.Is(err, models.ErrRunAsUserRoot) {
			t.Errorf("%q must be refused under a non-root mandate, got %v", v, err)
		}
	}
	for _, v := range []string{"1000", "1000:1000", "65534:0"} {
		if got, err := forced.validRunAsUser(1, v, false); err != nil || got != v {
			t.Errorf("%q should be accepted under a non-root mandate, got %q (%v)", v, got, err)
		}
	}
	// Blank stays blank — the profile's own UID applies, not a user's choice.
	if got, err := forced.validRunAsUser(1, "  ", false); err != nil || got != "" {
		t.Errorf("blank should stay blank, got %q (%v)", got, err)
	}
}
