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
	"slices"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/services/dnsprovider"
	"github.com/miabi-io/miabi/internal/storage/repositories"
)

func TestNormalizeZones(t *testing.T) {
	got, err := normalizeZones([]string{" Example.CD. ", "shop.example.cd", "example.cd"})
	if err != nil || !slices.Equal(got, []string{"example.cd", "shop.example.cd"}) {
		t.Fatalf("normalize: %v %v", got, err)
	}
	for _, bad := range [][]string{{"localhost"}, {"exa mple.cd"}, {"-x.cd"}, {"*.example.cd"}, {"example.cd/x"}} {
		if _, err := normalizeZones(bad); !errors.Is(err, ErrDNSZones) {
			t.Errorf("%v accepted: %v", bad, err)
		}
	}
	if _, err := normalizeZones(make([]string, maxDNSZones+1)); !errors.Is(err, ErrDNSZones) {
		t.Error("more than the zone limit accepted")
	}
}

type recordingConnector struct{ calls []uint }

func (r *recordingConnector) ConnectToProvider(workspaceID uint, _ *models.DNSProvider) (int, error) {
	r.calls = append(r.calls, workspaceID)
	return 0, nil
}

func TestConsoleDNSProvider(t *testing.T) {
	ctx := context.Background()
	db := accountsDB(t)
	if err := db.AutoMigrate(&models.Setting{}, &models.DNSProvider{}, &models.Domain{}, &models.DNSRecord{}); err != nil {
		t.Fatal(err)
	}
	crypto.Init("dns-test-secret-0123456789abcdef0123")
	settings := repositories.NewSettingRepository(db)
	s := NewService(db, repositories.NewUserRepository(db), nil, repositories.NewPlanRepository(db), settings, EnrolmentInfo{})
	s.SetAccountDeps(AccountDeps{Workspaces: fakeCreator{db}})
	db.Create(&models.Plan{Name: "pro", IsActive: true})
	for _, ext := range []string{"cus_1", "cus_2"} {
		if _, err := s.UpsertAccount(ctx, ext, AccountUpsert{Owner: Owner{Email: ext + "@acme.test"}, CreateWorkspace: &CreateWorkspace{Name: "ws-" + ext}}); err != nil {
			t.Fatal(err)
		}
	}
	providers := dnsprovider.NewService(repositories.NewDNSProviderRepository(db), repositories.NewDNSRecordRepository(db), repositories.NewDomainRepository(db))
	conn := &recordingConnector{}
	isCode := func(err error, want *CodedError) bool {
		var ce *CodedError
		return errors.As(err, &ce) && ce.Code() == want.Code()
	}
	push := DNSProviderPush{APIToken: "mdns_first", Zones: []string{"acme.cd"}}

	if _, err := s.PushDNSProvider(ctx, "cus_1", "ws-cus_1", push); !isCode(err, ErrDNSUnavailable) {
		t.Fatalf("without the DNS store: %v", err)
	}
	s.SetConsoleDNS(providers, conn)
	if _, err := s.PushDNSProvider(ctx, "cus_1", "ws-cus_1", push); !isCode(err, ErrNotConnected) {
		t.Fatalf("before enrolment: %v", err)
	}
	if err := settings.BulkUpsert([]models.Setting{{Key: SettingConsoleURL, Value: "https://console.example.com/", Type: models.SettingTypeString}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PushDNSProvider(ctx, "cus_1", "ws-cus_1", DNSProviderPush{Zones: []string{"acme.cd"}}); !isCode(err, ErrDNSToken) {
		t.Fatalf("first push without a token: %v", err)
	}
	if _, err := s.PushDNSProvider(ctx, "cus_1", "ws-cus_2", push); !isCode(err, ErrWorkspaceNotFound) {
		t.Fatalf("another account's workspace: %v", err)
	}

	v, err := s.PushDNSProvider(ctx, "cus_1", "ws-cus_1", push)
	if err != nil || v.Name != dnsprovider.ConsoleProviderName || v.Type != "console" || !slices.Equal(v.Zones, []string{"acme.cd"}) {
		t.Fatalf("first push: %+v %v", v, err)
	}
	if len(conn.calls) != 1 {
		t.Fatalf("domains not connected after the push: %v", conn.calls)
	}
	var ws models.Workspace
	db.Where("name = ?", "ws-cus_1").First(&ws)
	p, _ := providers.ConsoleProvider(ws.ID)
	if pub := providers.PublicCredentials(p); pub["server_url"] != "https://console.example.com/dns/api/v1" {
		t.Fatalf("API URL not derived from the connection: %v", pub)
	}

	v, err = s.PushDNSProvider(ctx, "cus_1", ws.UID, DNSProviderPush{Zones: []string{"acme.cd", "shop.cd"}})
	if err != nil || len(v.Zones) != 2 {
		t.Fatalf("zones-only update: %+v %v", v, err)
	}
	if _, err := providers.Provider(ws.ID, p.ID); err != nil {
		t.Fatalf("the stored token was lost on a zones-only update: %v", err)
	}
	if err := providers.Delete(ws.ID, p.ID); !errors.Is(err, dnsprovider.ErrManaged) {
		t.Fatalf("deleting from Miabi: %v", err)
	}
	if _, err := providers.Update(ctx, ws.ID, p.ID, dnsprovider.UpdateInput{}); !errors.Is(err, dnsprovider.ErrManaged) {
		t.Fatalf("editing from Miabi: %v", err)
	}
	if _, err := providers.Connect(ctx, ws.ID, dnsprovider.ConnectInput{Name: "mine", Type: "console", Credentials: map[string]string{"server_url": "x", "api_token": "y"}}); !errors.Is(err, dnsprovider.ErrInvalidType) {
		t.Fatalf("connecting the console type by hand: %v", err)
	}

	got, err := s.DNSProvider("cus_1", "ws-cus_1")
	if err != nil || got.Domains != 0 || len(got.Zones) != 2 {
		t.Fatalf("get: %+v %v", got, err)
	}
	if err := s.DeleteDNSProvider("cus_1", "ws-cus_1"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDNSProvider("cus_1", "ws-cus_1"); !isCode(err, ErrDNSNotConfigured) {
		t.Fatalf("second delete: %v", err)
	}

	var ws2 models.Workspace
	db.Where("name = ?", "ws-cus_2").First(&ws2)
	db.Create(&models.DNSProvider{WorkspaceID: ws2.ID, Name: dnsprovider.ConsoleProviderName, Type: "powerdns", CredentialsEnc: "x"})
	if _, err := s.PushDNSProvider(ctx, "cus_2", "ws-cus_2", push); !isCode(err, ErrDNSNameTaken) {
		t.Fatalf("a provider made in Miabi holds the name: %v", err)
	}

	if _, err := s.PushDNSProvider(ctx, "cus_1", "ws-cus_1", push); err != nil {
		t.Fatal(err)
	}
	if err := providers.DeleteAllConsole(); err != nil {
		t.Fatal(err)
	}
	if p, _ := providers.ConsoleProvider(ws.ID); p != nil {
		t.Fatal("DeleteAllConsole left a console provider")
	}
	var left int64
	db.Model(&models.DNSProvider{}).Where("workspace_id = ?", ws2.ID).Count(&left)
	if left != 1 {
		t.Fatal("DeleteAllConsole removed a provider made in Miabi")
	}
}
