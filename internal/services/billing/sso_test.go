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

package billing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/enterprise"
	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type hasEE struct {
	enterprise.EE
	flags map[string]bool
}

func (f hasEE) Has(flag string) bool { return f.flags[flag] }

func ssoService(t *testing.T, ee enterprise.EE) (*Service, *gorm.DB, *repositories.OAuthProviderRepository) {
	t.Helper()
	dsn := os.Getenv("MIABI_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set MIABI_TEST_POSTGRES_DSN to run the SSO provider test")
	}
	schema := fmt.Sprintf("t_billing_sso_%d", time.Now().UnixNano())
	root, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Exec("DROP SCHEMA " + schema + " CASCADE") })
	db, err := gorm.Open(postgres.Open(dsn+" search_path="+schema), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.Setting{}, &models.OAuthProvider{}, &models.OAuthGroupMapping{}, &models.UserIdentity{}); err != nil {
		t.Fatal(err)
	}
	crypto.Init("sso-test-secret-0123456789abcdef0123")
	settings := repositories.NewSettingRepository(db)
	if err := settings.BulkUpsert([]models.Setting{{Key: SettingConsoleURL, Value: "https://console.example.com", Type: models.SettingTypeString}}); err != nil {
		t.Fatal(err)
	}
	repo := repositories.NewOAuthProviderRepository(db)
	s := NewService(db, repositories.NewUserRepository(db), nil, nil, settings, EnrolmentInfo{MiabiURL: "https://paas.example.com"})
	s.SetAccountDeps(AccountDeps{EE: ee})
	s.SetSSO(repo, func(context.Context, *models.OAuthProvider) error { return nil }, "https://paas.example.com/api/v1")
	return s, db, repo
}

func TestSSOProviderCreateAndGroupSync(t *testing.T) {
	s, _, repo := ssoService(t, hasEE{flags: map[string]bool{enterprise.FlagSSOGroupMapping: true}})
	in := SSOProviderInput{Issuer: "https://console.example.com/", ClientID: "cid", ClientSecret: "secret",
		GroupsClaim: "groups", GroupPrefix: "miabi", AdminGroups: []string{"miabi-staff:admin"}}

	out, err := s.UpsertSSOProvider(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Name != models.ConsoleOAuthProviderName || out.DisplayName != "Miabi Console" || out.GroupSync != "on" || out.Adopted ||
		out.CallbackURL != "https://paas.example.com/api/v1/auth/oauth/miabi-console/callback" {
		t.Fatalf("provider = %+v", out)
	}
	p, _ := repo.FindByName(models.ConsoleOAuthProviderName)
	if !p.ConsoleManaged() || p.Issuer != "https://console.example.com" || p.GroupPrefix != "miabi" || p.ClientSecretEnc == "secret" {
		t.Fatalf("stored provider = %+v", p)
	}
	maps, _ := repo.ListMappings(p.ID)
	if len(maps) != 1 || maps[0].Group != "miabi-staff:admin" || !maps[0].SystemAdmin {
		t.Fatalf("mappings = %+v", maps)
	}

	in.ClientSecret = "rotated"
	if _, err := s.UpsertSSOProvider(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if n, _ := repo.Count(); n != 1 {
		t.Fatalf("providers after a second push = %d, want 1", n)
	}
}

func TestSSOProviderGroupSyncUnlicensed(t *testing.T) {
	s, _, _ := ssoService(t, hasEE{})
	out, err := s.UpsertSSOProvider(t.Context(), SSOProviderInput{Issuer: "https://console.example.com", ClientID: "c", ClientSecret: "s", GroupsClaim: "groups"})
	if err != nil {
		t.Fatal(err)
	}
	if out.GroupSync != "unlicensed" {
		t.Fatalf("group_sync = %q, want unlicensed", out.GroupSync)
	}
}

func TestSSOProviderAdoptsHandConfigured(t *testing.T) {
	s, db, repo := ssoService(t, hasEE{})
	hand := &models.OAuthProvider{Name: "console", DisplayName: "Console", Type: models.OAuthProviderOIDC, ClientID: "old", ClientSecretEnc: "x",
		Issuer: "https://console.example.com", Enabled: true}
	if err := repo.Create(hand); err != nil {
		t.Fatal(err)
	}
	user := &models.User{Email: "a@example.com", Username: "a", Name: "A", PasswordHash: "x"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.UserIdentity{UserID: user.ID, ProviderID: hand.ID, Subject: "sub-1"}).Error; err != nil {
		t.Fatal(err)
	}

	out, err := s.UpsertSSOProvider(t.Context(), SSOProviderInput{Issuer: "https://console.example.com", ClientID: "cid", ClientSecret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Adopted {
		t.Fatal("adopted = false, want the hand-configured provider taken over")
	}
	p, err := repo.FindByID(hand.ID)
	if err != nil || p.Name != models.ConsoleOAuthProviderName || !p.ConsoleManaged() {
		t.Fatalf("adopted provider = %+v %v", p, err)
	}
	if n, _ := repo.Count(); n != 1 {
		t.Fatalf("providers = %d, want 1", n)
	}
}

func TestSSOProviderRefusals(t *testing.T) {
	s, _, repo := ssoService(t, hasEE{})
	if _, err := s.UpsertSSOProvider(t.Context(), SSOProviderInput{Issuer: "https://evil.example.com", ClientID: "c", ClientSecret: "s"}); !errors.Is(err, ErrSSOIssuer) {
		t.Errorf("issuer on another host: %v, want ErrSSOIssuer", err)
	}
	if err := repo.Create(&models.OAuthProvider{Name: models.ConsoleOAuthProviderName, Type: models.OAuthProviderOIDC, ClientID: "c", ClientSecretEnc: "x",
		Issuer: "https://idp.example.org"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertSSOProvider(t.Context(), SSOProviderInput{Issuer: "https://console.example.com", ClientID: "c", ClientSecret: "s"}); !errors.Is(err, ErrSSONameTaken) {
		t.Errorf("name held by another issuer: %v, want ErrSSONameTaken", err)
	}
	if err := s.DeleteSSOProvider(); !errors.Is(err, ErrSSONotConfigured) {
		t.Errorf("deleting a hand-configured provider: %v, want ErrSSONotConfigured", err)
	}
}

func TestDisconnectLockout(t *testing.T) {
	s, db, repo := ssoService(t, hasEE{})
	if _, err := s.UpsertSSOProvider(t.Context(), SSOProviderInput{Issuer: "https://console.example.com", ClientID: "c", ClientSecret: "s"}); err != nil {
		t.Fatal(err)
	}
	p, _ := repo.FindByName(models.ConsoleOAuthProviderName)
	sso := &models.User{Email: "staff@example.com", Username: "staff", Name: "Staff", PasswordHash: "x", AuthSource: models.AuthSourceOAuth}
	local := &models.User{Email: "local@example.com", Username: "local", Name: "Local", PasswordHash: "x"}
	for _, u := range []*models.User{sso, local} {
		if err := db.Create(u).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.UserIdentity{UserID: u.ID, ProviderID: p.ID, Subject: u.Username}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if locked, err := s.wouldLockOut(sso.ID); err != nil || !locked {
		t.Errorf("SSO-only console user: locked=%v err=%v, want locked", locked, err)
	}
	if locked, _ := s.wouldLockOut(local.ID); locked {
		t.Error("local account reported as locked out")
	}
	other := &models.OAuthProvider{Name: "google", Type: models.OAuthProviderGoogle, ClientID: "g", ClientSecretEnc: "x", Enabled: true}
	if err := repo.Create(other); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.UserIdentity{UserID: sso.ID, ProviderID: other.ID, Subject: "g-1"}).Error; err != nil {
		t.Fatal(err)
	}
	if locked, _ := s.wouldLockOut(sso.ID); locked {
		t.Error("user with another enabled provider reported as locked out")
	}
}
