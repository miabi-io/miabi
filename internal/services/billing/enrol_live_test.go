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
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/auth"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// TestLiveEnrolment enrols against a real Miabi Console. Run it with a fresh code from the
// console's Admin → Instances page: MIABI_TEST_CONSOLE_CODE=mce1.… and MIABI_TEST_POSTGRES_DSN.
func TestLiveEnrolment(t *testing.T) {
	rawCode, dsn := os.Getenv("MIABI_TEST_CONSOLE_CODE"), os.Getenv("MIABI_TEST_POSTGRES_DSN")
	if rawCode == "" || dsn == "" {
		t.Skip("set MIABI_TEST_CONSOLE_CODE and MIABI_TEST_POSTGRES_DSN to enrol against a real console")
	}
	schema := fmt.Sprintf("t_billing_live_%d", time.Now().UnixNano())
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
	crypto.Init("live-enrolment-test-secret")
	s := NewService(db, repositories.NewUserRepository(db), auth.NewAPIKeyService(repositories.NewAPIKeyRepository(db)), nil,
		repositories.NewSettingRepository(db), EnrolmentInfo{MiabiURL: "http://localhost:9999", InstallID: "mbi_live_test", Version: "v0.0.0-test"})
	conn, err := s.Enroll(t.Context(), rawCode, 1)
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	code, _ := DecodeCode(rawCode)
	if !conn.Connected || conn.ConsoleURL != code.ConsoleURL || !strings.HasPrefix(conn.NotifyURL, code.ConsoleURL) {
		t.Fatalf("connection: %+v", conn)
	}
	t.Logf("enrolled with %s as instance %s; notify %s", conn.ConsoleURL, conn.ConsoleInstanceID, conn.NotifyURL)
}
