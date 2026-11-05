//go:build !enterprise

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
	"errors"
	"testing"
)

// TestCommunityLDAPDisabled verifies the Community build links no LDAP
// authenticator (so the go-ldap client is never compiled in) and that the
// sso_ldap flag is denied.
func TestCommunityLDAPDisabled(t *testing.T) {
	ee := New(nil, "", "", "")
	if ee.LDAP() != nil {
		t.Error("Community LDAP() must be nil")
	}
	if err := ee.Require(FlagSSOLDAP); !errors.Is(err, ErrLicenseRequired) {
		t.Errorf("Require(sso_ldap) = %v, want ErrLicenseRequired", err)
	}
	if ee.Has(FlagSSOLDAP) {
		t.Error("Community must not have sso_ldap")
	}
}

// TestSSOLDAPFlagRegistered ensures the flag is in the canonical AllFlags list
// (so the issuer tool + docs advertise it).
func TestSSOLDAPFlagRegistered(t *testing.T) {
	if !IsKnownFlag(FlagSSOLDAP) {
		t.Errorf("%q missing from AllFlags", FlagSSOLDAP)
	}
}
