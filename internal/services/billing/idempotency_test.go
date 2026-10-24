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
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/miabi-io/miabi/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestIdempotency(t *testing.T) {
	dsn := os.Getenv("MIABI_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set MIABI_TEST_POSTGRES_DSN to run the idempotency test")
	}
	schema := fmt.Sprintf("t_billing_idem_%d", time.Now().UnixNano())
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
	if err := db.AutoMigrate(&models.BillingIdempotencyKey{}); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	s := &Service{db: db, now: func() time.Time { return clock }}
	h1, h2 := RequestHash("PUT", "/a", []byte(`{"x":1}`)), RequestHash("PUT", "/a", []byte(`{"x":2}`))

	if st, err := s.BeginIdempotent("k1", h1); st != nil || err != nil {
		t.Fatalf("first claim: %v %v", st, err)
	}
	if _, err := s.BeginIdempotent("k1", h1); !errors.Is(err, ErrInFlight) {
		t.Fatalf("while running: %v", err)
	}
	if err := s.FinishIdempotent("k1", 200, []byte(`{"success":true}`)); err != nil {
		t.Fatal(err)
	}
	if st, err := s.BeginIdempotent("k1", h1); err != nil || st == nil || st.Status != 200 || string(st.Body) != `{"success":true}` {
		t.Fatalf("replay: %+v %v", st, err)
	}
	if _, err := s.BeginIdempotent("k1", h2); !errors.Is(err, ErrKeyReused) {
		t.Fatalf("different request: %v", err)
	}

	// A server error is not final: the key is released and the retry runs.
	if _, err := s.BeginIdempotent("k2", h1); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishIdempotent("k2", 503, nil); err != nil {
		t.Fatal(err)
	}
	if st, err := s.BeginIdempotent("k2", h1); st != nil || err != nil {
		t.Fatalf("retry after 503: %v %v", st, err)
	}

	// A request that never finished is taken over after a while.
	clock = clock.Add(abandonAfter + time.Second)
	if st, err := s.BeginIdempotent("k2", h1); st != nil || err != nil {
		t.Fatalf("abandoned key: %v %v", st, err)
	}

	clock = clock.Add(IdempotencyTTL)
	if err := s.PurgeIdempotency(); err != nil {
		t.Fatal(err)
	}
	var n int64
	db.Model(&models.BillingIdempotencyKey{}).Count(&n)
	if n != 0 {
		t.Fatalf("%d keys left after the purge", n)
	}
}
