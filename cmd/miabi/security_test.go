// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"testing"

	"github.com/miabi-io/miabi/internal/config"
	"github.com/miabi-io/miabi/internal/worker"
)

// The official-template exemption is about the UID and nothing else. It used to collapse into an
// empty profile, so an app from an official template also lost no-new-privileges and the NET_RAW
// drop — strictly less hardening than its workspace asked for.
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

	// An unrestricted workspace keeps the image's user and its setuid binaries — but not NET_RAW,
	// which is the baseline rather than part of the restricted profile.
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

// NET_RAW is dropped for every workspace on every path, so a tenant cannot forge ARP on a bridge it
// shares with other tenants. It is not part of the restricted opt-in, so it holds even on an
// install that never configured a restricted UID.
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

// The baseline is not the restricted profile: it must not pin a user, set no-new-privileges (which
// breaks setuid binaries such as sudo and some ping builds), or mark the workload restricted —
// that last one would refuse every capability grant, including the NET_RAW grant that is the
// documented way to get raw sockets back.
func TestBaselineIsNotTheRestrictedProfile(t *testing.T) {
	b := baselineSecurity()
	if b.User != "" || b.NoNewPrivileges || b.Restricted {
		t.Fatalf("baseline = %+v, want only the capability drop", b)
	}
}

// An install with no configured UID still gets the baseline: the resolver used to be nil there,
// which meant no profile was applied at all.
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
