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
	"context"

	"gorm.io/gorm"
)

// New returns the Community-Edition stub: it denies every licensed feature and links none of the
// license-verification code. This file is the only implementation compiled without the
// `enterprise` build tag.
func New(_ *gorm.DB, _ string, _ string, _ string) EE { return ceStub{} }

type ceStub struct{}

func (ceStub) Entitlements() Entitlements {
	return Entitlements{Edition: EditionCommunity, State: "none", Flags: map[string]bool{}, Limits: map[string]int{}}
}
func (ceStub) Has(string) bool             { return false }
func (ceStub) Mutable(string) bool         { return false }
func (ceStub) Require(string) error        { return ErrLicenseRequired }
func (ceStub) RequireMutable(string) error { return ErrLicenseRequired }
func (ceStub) Install(context.Context, string, string) (Entitlements, error) {
	return Entitlements{}, ErrCommunityEdition
}
func (ceStub) Remove(context.Context) error { return ErrCommunityEdition }
func (ceStub) InitSSO(SSODeps)              {}
func (ceStub) SAML() SAMLProvider           { return nil }
func (ceStub) SCIM() SCIMProvider           { return nil }
func (ceStub) LDAP() LDAPAuthenticator      { return nil }
