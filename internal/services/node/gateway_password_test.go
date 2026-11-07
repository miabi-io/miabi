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

package node

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
	"github.com/miabi-io/miabi/internal/storage/repositories"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type passwordRow struct {
	ID                      uint `gorm:"primaryKey"`
	Name                    string
	GatewayRedisPasswordEnc string
}

func (passwordRow) TableName() string { return "servers" }

func TestGatewayRedisPasswordIsStableAndWriteOnce(t *testing.T) {
	crypto.Init("test-master-key")
	t.Cleanup(func() { crypto.Init("") })
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&passwordRow{}); err != nil {
		t.Fatal(err)
	}
	db.Create(&passwordRow{ID: 1, Name: "edge-1"})
	repo := repositories.NewServerRepository(db)
	s := NewService(repo, nil)

	first, err := s.GatewayRedisPassword(1)
	if err != nil || first == "" {
		t.Fatalf("first = %q, %v", first, err)
	}
	again, err := s.GatewayRedisPassword(1)
	if err != nil || again != first {
		t.Fatalf("second call returned a different password (%v)", err)
	}

	// A second writer that lost the race must not replace the stored password.
	enc, _ := crypto.Encrypt("intruder")
	if stored, err := repo.ClaimGatewayRedisPassword(1, enc, false); err != nil || stored {
		t.Fatalf("claim over an existing password: stored=%v err=%v", stored, err)
	}
	if got, _ := s.GatewayRedisPassword(1); got != first {
		t.Fatal("the stored password changed")
	}
}

type sqlCapture struct {
	logger.Interface
	sql []string
}

func (c *sqlCapture) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	s, _ := fc()
	c.sql = append(c.sql, s)
}

// A full-row save of a stale node would blank the password, and the next deploy would mint one Redis does not know.
func TestServerUpdateNeverWritesTheRedisPassword(t *testing.T) {
	capture := &sqlCapture{Interface: logger.Discard}
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory"), &gorm.Config{DryRun: true, Logger: capture})
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.NewServerRepository(db).Update(&models.Server{ID: 1, Name: "edge-1"}); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(capture.sql, "\n")
	if !strings.Contains(all, "UPDATE") {
		t.Fatalf("no update issued: %s", all)
	}
	if strings.Contains(all, "gateway_redis_password_enc") {
		t.Fatalf("Update writes the gateway Redis password: %s", all)
	}
}

// Minting here is how a reinstalled node got a password its running Redis never received.
func TestStoredGatewayRedisPasswordNeverMints(t *testing.T) {
	crypto.Init("test-master-key")
	t.Cleanup(func() { crypto.Init("") })
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&passwordRow{}); err != nil {
		t.Fatal(err)
	}
	db.Create(&passwordRow{ID: 1, Name: "edge-1"})
	s := NewService(repositories.NewServerRepository(db), nil)

	if pw, err := s.StoredGatewayRedisPassword(1); err != nil || pw != "" {
		t.Fatalf("before any deploy: %q, %v", pw, err)
	}
	var row passwordRow
	db.First(&row, 1)
	if row.GatewayRedisPasswordEnc != "" {
		t.Fatal("reading the password minted one")
	}
	minted, _ := s.GatewayRedisPassword(1)
	if pw, _ := s.StoredGatewayRedisPassword(1); pw != minted {
		t.Fatal("stored password differs from the minted one")
	}
}
