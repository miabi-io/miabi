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

package upgrade

import (
	"context"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type ipv6ClusterFixture struct {
	ID          uint `gorm:"primaryKey"`
	IngressIP   string
	IngressIPv6 string `gorm:"column:ingress_ipv6"`
}

func (ipv6ClusterFixture) TableName() string { return "clusters" }

// An IPv6 address stored as the cluster's IP moves to the IPv6 column; IPv4 and an IPv6 already set stay put.
func TestClusterIngressIPv6Step(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&ipv6ClusterFixture{}); err != nil {
		t.Fatal(err)
	}
	db.Create(&[]ipv6ClusterFixture{
		{ID: 1, IngressIP: "203.0.113.1"},
		{ID: 2, IngressIP: "2001:db8::2"},
		{ID: 3, IngressIP: "2001:db8::3", IngressIPv6: "2001:db8::33"},
	})
	for range 2 {
		if err := clusterIngressIPv6Step(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}
	want := map[uint][2]string{
		1: {"203.0.113.1", ""},
		2: {"", "2001:db8::2"},
		3: {"2001:db8::3", "2001:db8::33"},
	}
	var rows []ipv6ClusterFixture
	db.Order("id").Find(&rows)
	for _, r := range rows {
		if got := [2]string{r.IngressIP, r.IngressIPv6}; got != want[r.ID] {
			t.Errorf("cluster %d = %v, want %v", r.ID, got, want[r.ID])
		}
	}
}
