//go:build enterprise

// Copyright (c) 2026 Jonas Kaninda. All rights reserved.
//
// This file is part of Miabi Enterprise Edition.
//
// This file is licensed under the Miabi Enterprise License, not the GNU Affero
// General Public License. You may use it only with a valid Miabi Enterprise
// subscription and under the terms in internal/enterprise/LICENSE.md.
//
// SPDX-License-Identifier: LicenseRef-Miabi-Enterprise

package enterprise

import (
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/enterprise/license"
)

func withClaims(notAfter time.Time, flags ...string) *impl {
	return &impl{claims: &license.Claims{
		LicenseID: "lic_t", Edition: EditionEnterprise, Flags: flags,
		Limits:    map[string]int{"node_limit": 4},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter, GraceDays: 14,
	}}
}

func TestGate_Valid(t *testing.T) {
	e := withClaims(time.Now().Add(48*time.Hour), FlagSSOSAML)
	if !e.Has(FlagSSOSAML) || !e.Mutable(FlagSSOSAML) {
		t.Fatal("entitled flag should be usable and mutable while valid")
	}
	if err := e.Require(FlagSSOSAML); err != nil {
		t.Fatalf("Require: %v", err)
	}
	if err := e.RequireMutable(FlagSSOSAML); err != nil {
		t.Fatalf("RequireMutable: %v", err)
	}
	// Entitled license but missing flag → entitlement denied (403), not 402.
	if err := e.Require(FlagHA); err != ErrEntitlementDenied {
		t.Fatalf("missing flag: got %v, want ErrEntitlementDenied", err)
	}
}

func TestGate_Degraded_ReadOnly(t *testing.T) {
	// Expired beyond grace: runtime use still works, but config is frozen.
	e := withClaims(time.Now().Add(-30*24*time.Hour), FlagSSOSAML)
	if e.Entitlements().State != "degraded" {
		t.Fatalf("state = %s, want degraded", e.Entitlements().State)
	}
	if !e.Has(FlagSSOSAML) {
		t.Fatal("degraded license should keep entitled features running (Has=true)")
	}
	if e.Mutable(FlagSSOSAML) {
		t.Fatal("degraded license must freeze config (Mutable=false)")
	}
	if err := e.RequireMutable(FlagSSOSAML); err != ErrLicenseExpired {
		t.Fatalf("RequireMutable on degraded: got %v, want ErrLicenseExpired", err)
	}
}

func TestGate_Community(t *testing.T) {
	e := &impl{} // no claims
	if e.Has(FlagSSOSAML) || e.Mutable(FlagSSOSAML) {
		t.Fatal("community must deny everything")
	}
	if err := e.Require(FlagSSOSAML); err != ErrLicenseRequired {
		t.Fatalf("community Require: got %v, want ErrLicenseRequired", err)
	}
	if e.Entitlements().Edition != EditionCommunity {
		t.Fatalf("edition = %s, want community", e.Entitlements().Edition)
	}
}

// An untrusted signing key grants nothing and says why instead of passing for Community.
func TestUnknownSigningKey(t *testing.T) {
	e := &impl{unknownKID: "miabi-209901"}
	if e.Has(FlagSSOSAML) {
		t.Fatal("an untrusted key must not grant features")
	}
	ent := e.Entitlements()
	if ent.State != StateUnknownSigningKey || ent.SigningKey != "miabi-209901" {
		t.Fatalf("entitlements = %+v", ent)
	}
}
