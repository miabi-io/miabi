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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/auth"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// fakeConsole answers POST /api/v1/instances/enroll the way the console does.
type fakeConsole struct {
	mu        sync.Mutex
	got       enrollRequest
	refuse    string // a contract error code to answer with
	elsewhere bool   // answer with a notify_url on another host
	redirect  bool
}

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (f *fakeConsole) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.redirect {
		http.Redirect(w, r, "https://evil.example/steal", http.StatusTemporaryRedirect)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/api/v1/instances/enroll" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_ = json.NewDecoder(r.Body).Decode(&f.got)
	if f.refuse != "" {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": map[string]any{"code": f.refuse, "message": "enrolment token has already been used"}})
		return
	}
	base := "http://" + r.Host
	notify := base + "/api/v1/miabi/events"
	if f.elsewhere {
		notify = "https://elsewhere.example/api/v1/miabi/events"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{
		"notify_url": notify, "portal_session_endpoint": base + "/api/v1/miabi/portal-sessions",
		"portal_allowed_hosts": []string{"127.0.0.1"}, "console_url": base}})
}

func code(u, i, t string) string {
	raw, _ := json.Marshal(Code{ConsoleURL: u, InstanceID: i, Token: t})
	return CodePrefix + base64.RawURLEncoding.EncodeToString(raw)
}

func TestDecodeCode(t *testing.T) {
	for in, ok := range map[string]bool{
		code("https://console.example", "i1", "t1"):                                         true,
		"  " + code("http://localhost:8080", "i1", "t1"):                                    true,
		code("http://console.example", "i1", "t1"):                                          false, // plain http off localhost
		code("https://user:pw@console.example", "i1", "t1"):                                 false,
		code("https://console.example", "", "t1"):                                           false,
		"mce2." + strings.TrimPrefix(code("https://console.example", "i", "t"), CodePrefix): false,
		"mce1.!!!": false,
	} {
		if _, err := DecodeCode(in); (err == nil) != ok {
			t.Errorf("DecodeCode(%q) err = %v, want ok=%v", in, err, ok)
		}
	}
}

func TestEnrolment(t *testing.T) {
	dsn := os.Getenv("MIABI_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set MIABI_TEST_POSTGRES_DSN to run the enrolment test")
	}
	schema := fmt.Sprintf("t_billing_enrol_%d", time.Now().UnixNano())
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
	if err := db.AutoMigrate(&models.User{}, &models.APIKey{}, &models.Setting{}); err != nil {
		t.Fatal(err)
	}
	crypto.Init("enrolment-test-secret-0123456789abcdef")
	keys := auth.NewAPIKeyService(repositories.NewAPIKeyRepository(db))
	s := NewService(db, repositories.NewUserRepository(db), keys, nil, repositories.NewSettingRepository(db),
		EnrolmentInfo{MiabiURL: "https://paas.example.com", InstallID: "mbi_test", Version: "v9.9.9"})
	fake := &fakeConsole{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	keyCount := func(revoked bool) int64 {
		var n int64
		db.Model(&models.APIKey{}).Where("revoked = ?", revoked).Count(&n)
		return n
	}

	// A refusal or an answer pointing elsewhere leaves no usable key behind.
	fake.refuse = "ENROLL_ALREADY_USED"
	var ce *CodedError
	if _, err := s.Enroll(t.Context(), code(srv.URL, "inst-1", "tok"), 1); !errors.As(err, &ce) || ce.Code() != "ENROLL_ALREADY_USED" || ce.Status != http.StatusConflict {
		t.Fatalf("refused: %v", err)
	}
	fake.refuse, fake.elsewhere = "", true
	if _, err := s.Enroll(t.Context(), code(srv.URL, "inst-1", "tok"), 1); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("answer on another host: %v", err)
	}
	fake.elsewhere, fake.redirect = false, true
	if _, err := s.Enroll(t.Context(), code(srv.URL, "inst-1", "tok"), 1); err == nil {
		t.Fatal("followed a redirect")
	}
	fake.redirect = false
	if keyCount(false) != 0 || keyCount(true) != 3 {
		t.Fatalf("keys after failed attempts: %d active, %d revoked", keyCount(false), keyCount(true))
	}

	conn, err := s.Enroll(t.Context(), code(srv.URL, "inst-1", "tok"), 42)
	if err != nil {
		t.Fatal(err)
	}
	got := fake.got
	if got.InstanceID != "inst-1" || got.Token != "tok" || got.MiabiURL != "https://paas.example.com" || got.InstallID != "mbi_test" ||
		got.Version != "v9.9.9" || !strings.HasPrefix(got.APIKey, "mb_") || !hex64.MatchString(got.NotifySecret) || !hex64.MatchString(got.PortalSigningSecret) {
		t.Fatalf("request to the console: %+v", got)
	}
	if ts, err := time.Parse(time.RFC3339, got.Timestamp); err != nil || time.Since(ts) > time.Minute {
		t.Fatalf("timestamp %q", got.Timestamp)
	}
	if !conn.Connected || conn.ConsoleURL != srv.URL || conn.ConsoleInstanceID != "inst-1" || conn.ConnectedBy != 42 || conn.KeyPrefix == "" {
		t.Fatalf("connection: %+v", conn)
	}
	minted, err := keys.Verify(got.APIKey)
	if err != nil || minted.Purpose() != models.KeyPurposeBilling {
		t.Fatalf("the key handed to the console: %+v %v", minted, err)
	}
	secret, err := s.NotifySecret()
	if err != nil || secret != got.NotifySecret {
		t.Fatalf("notify secret round trip: %v", err)
	}
	var stored models.Setting
	db.Where("key = ?", SettingNotifySecret).First(&stored)
	if stored.Value == got.NotifySecret || !crypto.IsEncrypted(stored.Value) {
		t.Fatal("the notify secret is stored in clear")
	}
	if _, err := s.Enroll(t.Context(), code(srv.URL, "inst-2", "tok2"), 42); !errors.Is(err, ErrAlreadyConnected) {
		t.Fatalf("second connection: %v", err)
	}

	if err := s.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.Verify(got.APIKey); err == nil {
		t.Fatal("the billing key still works after disconnecting")
	}
	if c, _ := s.Connection(); c.Connected {
		t.Fatalf("still connected: %+v", c)
	}
	if err := s.Disconnect(); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("disconnect twice: %v", err)
	}
}
