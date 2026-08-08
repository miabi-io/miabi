//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package enterprise

import (
	"context"

	"gorm.io/gorm"
)

// New returns the Community-Edition stub: it denies every licensed feature and links none of the
// license-verification code. This file is the only implementation compiled without the
// `enterprise` build tag.
func New(_ *gorm.DB, _ string, _ string, _ string, _ string) EE { return ceStub{} }

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
