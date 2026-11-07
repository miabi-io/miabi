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

package oauth

import (
	"context"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// Provider-created accounts are the provider's to name, so self-service profile edits refuse.
func TestAuthenticate_StampsTheOAuthAuthSource(t *testing.T) {
	s, db, idp := registerService(t, "new@acme.test")

	u, err := s.Authenticate(context.Background(), providerFor(idp, nil), "code", "http://localhost/cb")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if u.AuthSource != models.AuthSourceOAuth {
		t.Fatalf("auth source = %q, want %q", u.AuthSource, models.AuthSourceOAuth)
	}
	if !u.IsExternal() {
		t.Fatal("a provider-registered account must read as externally managed")
	}

	var stored models.User
	if err := db.First(&stored, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.AuthSource != models.AuthSourceOAuth {
		t.Fatalf("stored auth source = %q, want %q", stored.AuthSource, models.AuthSourceOAuth)
	}
}

// Email matching is linking, not provisioning: a local user who also uses SSO keeps an editable profile.
func TestAuthenticate_LeavesAnExistingAccountsAuthSource(t *testing.T) {
	s, db, idp := registerService(t, "local@acme.test")

	existing := &models.User{
		Name: "Local", Email: "local@acme.test", PasswordHash: "x",
		Role: models.SystemRoleUser, Active: true, AuthSource: models.AuthSourceLocal,
	}
	if err := db.Create(existing).Error; err != nil {
		t.Fatal(err)
	}

	u, err := s.Authenticate(context.Background(), providerFor(idp, nil), "code", "http://localhost/cb")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if u.ID != existing.ID {
		t.Fatalf("signed in as user %d, want the existing %d", u.ID, existing.ID)
	}
	if u.IsExternal() {
		t.Fatalf("auth source = %q, want it left local", u.AuthSource)
	}
}

// The SAML just-in-time path stamps its own source for the same reason.
func TestProvisionSSOUser_StampsTheSAMLAuthSource(t *testing.T) {
	s, _, _ := registerService(t, "saml@acme.test")

	u, err := s.ProvisionSSOUser(context.Background(), "saml@acme.test", "SAML User", "saml-user")
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if u.AuthSource != models.AuthSourceSAML {
		t.Fatalf("auth source = %q, want %q", u.AuthSource, models.AuthSourceSAML)
	}
}
