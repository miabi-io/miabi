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

package domain

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type fakeConsoleDNS struct{ p *models.DNSProvider }

func (f fakeConsoleDNS) ConsoleProviderFor(_ uint, name string) *uint {
	if f.p.CoversDomain(name) {
		return &f.p.ID
	}
	return nil
}

type fakeAutomator struct{ published chan string }

func (fakeAutomator) ProviderExists(uint, uint) bool   { return true }
func (fakeAutomator) RecordsSupported(uint, uint) bool { return true }
func (f fakeAutomator) EnsureVerificationRecord(_ context.Context, d *models.Domain) error {
	f.published <- d.Name
	return nil
}
func (fakeAutomator) CleanupDomain(context.Context, *models.Domain) error { return nil }

func TestConsoleZonesConnectAndVerify(t *testing.T) {
	dsn := os.Getenv("MIABI_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set MIABI_TEST_POSTGRES_DSN to run the console DNS domain test")
	}
	schema := fmt.Sprintf("t_domain_console_%d", time.Now().UnixNano())
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
	if err := db.AutoMigrate(&models.User{}, &models.Plan{}, &models.Workspace{}, &models.DNSProvider{}, &models.Domain{}); err != nil {
		t.Fatal(err)
	}
	owner := models.User{Email: "jane@acme.test", Name: "Jane"}
	db.Create(&owner)
	ws := models.Workspace{Name: "acme", OwnerID: owner.ID}
	if err := db.Create(&ws).Error; err != nil {
		t.Fatal(err)
	}
	prov := models.DNSProvider{WorkspaceID: ws.ID, Name: "miabi-console", Type: "console", ManagedBy: models.ManagedByConsole,
		Zones: []string{"acme.cd"}}
	if err := db.Create(&prov).Error; err != nil {
		t.Fatal(err)
	}

	s := NewService(repositories.NewDomainRepository(db))
	published := make(chan string, 8)
	s.SetDNSAutomator(fakeAutomator{published: published})
	s.SetConsoleDNS(fakeConsoleDNS{p: &prov})
	s.lookup = func(_ context.Context, host string) ([]string, error) {
		var d models.Domain
		if err := db.Where("? = '_miabi-challenge.' || name", host).First(&d).Error; err != nil {
			return nil, nil
		}
		return []string{d.ChallengeValue()}, nil
	}

	other, err := s.Create(ws.ID, Input{Name: "other.example"})
	if err != nil || other.DNSProviderID != nil {
		t.Fatalf("a domain outside the zones stays manual: %+v %v", other, err)
	}
	shop, err := s.Create(ws.ID, Input{Name: "shop.acme.cd"})
	if err != nil || shop.DNSProviderID == nil || *shop.DNSProviderID != prov.ID {
		t.Fatalf("a domain under a console zone connects: %+v %v", shop, err)
	}
	waitVerified(t, db, shop.ID, published)

	db.Model(&models.Domain{}).Where("id = ?", shop.ID).Updates(map[string]any{"dns_provider_id": nil, "verified": false})
	apex := models.Domain{WorkspaceID: ws.ID, Name: "acme.cd", VerificationToken: "tok-apex"}
	db.Create(&apex)
	n, err := s.ConnectToProvider(ws.ID, &prov)
	if err != nil || n != 2 {
		t.Fatalf("connected %d domains, want the apex and shop: %v", n, err)
	}
	waitVerified(t, db, apex.ID, published)
	var still models.Domain
	db.First(&still, other.ID)
	if still.DNSProviderID != nil {
		t.Fatal("ConnectToProvider touched a domain outside the zones")
	}
}

func waitVerified(t *testing.T, db *gorm.DB, id uint, published chan string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		var d models.Domain
		db.First(&d, id)
		if d.Verified && d.VerifiedVia == models.VerifiedViaDNSProvider {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("%s was not verified in the background: %+v", d.Name, d)
		case <-published:
		case <-time.After(100 * time.Millisecond):
		}
	}
}
