// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// idpFor stands in for the provider's token and userinfo endpoints, asserting the given identity.
func idpFor(t *testing.T, email string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"t","token_type":"Bearer"}`))
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"email":"` + email + `","name":"Someone","preferred_username":"someone"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func registerService(t *testing.T, email string) (*Service, *gorm.DB, *httptest.Server) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.User{}); err != nil {
		t.Fatal(err)
	}
	idp := idpFor(t, email)
	return &Service{users: repositories.NewUserRepository(db), http: idp.Client()}, db, idp
}

// providerFor builds an enabled auto-registering provider pointed at the stub identity provider.
// The client secret is stored in the clear, which crypto.Decrypt passes through without a key.
func providerFor(idp *httptest.Server, org *uint) *models.OAuthProvider {
	return &models.OAuthProvider{
		Name: "acme-sso", Type: models.OAuthProviderOIDC,
		ClientID: "id", ClientSecretEnc: "secret",
		TokenURL: idp.URL + "/token", UserInfoURL: idp.URL + "/userinfo",
		Enabled: true, AutoRegister: true,
		OrganizationID: org,
	}
}

// A provider registers the accounts it creates into its own organization; unattached, they stay in
// the default one, which is how every install behaved before providers could be attached at all.
func TestAuthenticate_RegistersIntoTheProvidersOrganization(t *testing.T) {
	acme := uint(7)
	for _, tc := range []struct {
		name string
		org  *uint
	}{
		{"attached to an organization", &acme},
		{"unattached leaves the default organization", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, idp := registerService(t, "new@acme.test")

			u, err := s.Authenticate(context.Background(), providerFor(idp, tc.org), "code", "http://localhost/cb")
			if err != nil {
				t.Fatalf("authenticate: %v", err)
			}
			if !sameOrgID(u.OrganizationID, tc.org) {
				t.Fatalf("organization = %v, want %v", u.OrganizationID, tc.org)
			}
		})
	}
}

// The guarantee that matters: signing in through a provider attached to one organization must never
// move an account that already belongs to another. Only a registration decides a realm.
func TestAuthenticate_NeverMovesAnExistingAccount(t *testing.T) {
	globex := uint(8)
	acme := uint(7)
	s, db, idp := registerService(t, "already@globex.test")

	existing := &models.User{
		Name: "Already", Email: "already@globex.test", PasswordHash: "x",
		Role: models.SystemRoleUser, Active: true, OrganizationID: &globex,
	}
	if err := db.Create(existing).Error; err != nil {
		t.Fatal(err)
	}

	u, err := s.Authenticate(context.Background(), providerFor(idp, &acme), "code", "http://localhost/cb")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if u.ID != existing.ID {
		t.Fatalf("signed in as user %d, want the existing %d", u.ID, existing.ID)
	}
	if !sameOrgID(u.OrganizationID, &globex) {
		t.Fatalf("organization = %v, want it left at %d", u.OrganizationID, globex)
	}

	var stored models.User
	if err := db.First(&stored, existing.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !sameOrgID(stored.OrganizationID, &globex) {
		t.Fatalf("stored organization = %v, want it left at %d", stored.OrganizationID, globex)
	}
}

func sameOrgID(a, b *uint) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
