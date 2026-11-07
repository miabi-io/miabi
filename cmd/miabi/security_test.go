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

package main

import (
	"testing"

	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/worker"
)

// The official-template exemption covers only the UID, not no-new-privileges or the NET_RAW drop.
func TestSecurityForExemptsOnlyTheUID(t *testing.T) {
	const uid = "100000:0"

	ordinary := securityFor(uid, true, false)
	if ordinary.User != uid || !ordinary.NoNewPrivileges || len(ordinary.CapDrop) == 0 || !ordinary.Restricted {
		t.Fatalf("restricted app = %+v, want the UID and the hardening", ordinary)
	}

	exempt := securityFor(uid, true, true)
	if exempt.User != "" {
		t.Errorf("exempt app User = %q, want empty — that is what the exemption grants", exempt.User)
	}
	if !exempt.NoNewPrivileges {
		t.Error("exempt app lost no-new-privileges, which the exemption does not cover")
	}
	if len(exempt.CapDrop) == 0 {
		t.Error("exempt app lost its capability drops, which the exemption does not cover")
	}
	if !exempt.Restricted {
		t.Error("exempt app no longer reports as restricted, so nothing downstream knows the profile applies")
	}

	// An unrestricted workspace keeps the image's user and setuid binaries, but NET_RAW stays dropped (baseline).
	for _, exemptUID := range []bool{false, true} {
		got := securityFor(uid, false, exemptUID)
		if got.User != "" || got.NoNewPrivileges || got.Restricted {
			t.Fatalf("unrestricted workspace got the restricted profile: %+v", got)
		}
		if !dropsNetRaw(got.CapDrop) {
			t.Fatalf("unrestricted workspace kept NET_RAW: %+v", got)
		}
	}
}

// NET_RAW is always dropped so a tenant cannot forge ARP on a shared bridge, even with no restricted UID configured.
func TestNetRawIsDroppedEverywhere(t *testing.T) {
	for _, tc := range []struct {
		name string
		sec  worker.Security
	}{
		{"baseline", baselineSecurity()},
		{"unrestricted workspace", securityFor("100000:0", false, false)},
		{"restricted workspace", securityFor("100000:0", true, false)},
		{"official-template app", securityFor("100000:0", true, true)},
	} {
		if !dropsNetRaw(tc.sec.CapDrop) {
			t.Errorf("%s: CapDrop = %v, want NET_RAW dropped", tc.name, tc.sec.CapDrop)
		}
	}
}

// Baseline must not set no-new-privileges (breaks setuid) or mark restricted (would refuse the NET_RAW grant).
func TestBaselineIsNotTheRestrictedProfile(t *testing.T) {
	b := baselineSecurity()
	if b.User != "" || b.NoNewPrivileges || b.Restricted {
		t.Fatalf("baseline = %+v, want only the capability drop", b)
	}
}

// An install with no configured UID still gets the baseline profile.
func TestResolverAppliesTheBaselineWithoutAConfiguredUID(t *testing.T) {
	r := newSecurityResolver(&config.Config{}, nil)
	if r == nil {
		t.Fatal("no resolver, so no baseline is applied")
	}
	sec := r.ContainerSecurity(1, false)
	if !dropsNetRaw(sec.CapDrop) {
		t.Errorf("CapDrop = %v, want NET_RAW dropped", sec.CapDrop)
	}
	if sec.User != "" || sec.NoNewPrivileges || sec.Restricted {
		t.Errorf("profile = %+v, want the baseline only — there is no UID to pin", sec)
	}
}

func dropsNetRaw(drop []string) bool {
	for _, c := range drop {
		if c == "NET_RAW" {
			return true
		}
	}
	return false
}
