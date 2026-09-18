// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"testing"

	"github.com/miabi-io/miabi/internal/config"
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

	// An unrestricted workspace is untouched either way.
	for _, exemptUID := range []bool{false, true} {
		if got := securityFor(uid, false, exemptUID); got.User != "" || got.NoNewPrivileges || len(got.CapDrop) != 0 {
			t.Fatalf("unrestricted workspace was hardened anyway: %+v", got)
		}
	}
}

// The resolver is nil without a configured UID, which is how an operator turns the whole thing off.
func TestNoResolverWithoutAConfiguredUID(t *testing.T) {
	if newSecurityResolver(&config.Config{}, nil) != nil {
		t.Fatal("a resolver was built with no RestrictedUID")
	}
}
