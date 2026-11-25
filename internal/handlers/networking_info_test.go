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

package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/jkaninda/okapi"
)

type fakePool struct{ used, total int }

func (f fakePool) Stats() (int, int) { return f.used, f.total }

// String order would put 10.64.10.0/24 before 10.64.2.0/24.
func TestSubnetsSortByAddress(t *testing.T) {
	subnets := []string{"10.64.10.0/24", "10.64.2.0/24", "10.64.100.0/24", "10.64.0.0/24"}
	sort.SliceStable(subnets, func(i, j int) bool { return subnetLess(subnets[i], subnets[j]) })
	want := []string{"10.64.0.0/24", "10.64.2.0/24", "10.64.10.0/24", "10.64.100.0/24"}
	for i := range want {
		if subnets[i] != want[i] {
			t.Fatalf("order = %v, want %v", subnets, want)
		}
	}
}

func TestNetworkingInfoReportsPoolUsageOnlyWithAnAllocator(t *testing.T) {
	get := func(h *NetworkingHandler) NetworkingInfo {
		app := okapi.New()
		app.Get("/networking", h.Info)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/networking", nil))
		var out struct {
			Data NetworkingInfo `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Data
	}
	on := get(NewNetworkingHandler(NetworkingInfo{PoolCIDR: "10.64.0.0/12"}, fakePool{used: 50, total: 4096}, nil))
	if !on.PoolEnabled || on.PoolUsed != 50 || on.PoolTotal != 4096 || on.ProxySubnet == "" || on.ProxySubnetSource == "" {
		t.Errorf("with allocator = %+v", on)
	}
	if off := get(NewNetworkingHandler(NetworkingInfo{}, nil, nil)); off.PoolEnabled || off.PoolTotal != 0 {
		t.Errorf("without allocator = %+v", off)
	}
}
