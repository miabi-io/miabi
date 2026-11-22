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

package main

import (
	"testing"

	"github.com/miabi-io/miabi/internal/services/registryserver"
)

func TestRegistryQuotaBreach(t *testing.T) {
	const mib = 1024 * 1024
	cases := []struct {
		name    string
		usage   *registryserver.Usage
		limitMB int
		want    bool
	}{
		{"no quota", &registryserver.Usage{TotalBytes: 900 * mib}, 0, false},
		{"never counted", nil, 1000, false},
		{"below", &registryserver.Usage{TotalBytes: 899 * mib}, 1000, false},
		{"at 90%", &registryserver.Usage{TotalBytes: 900 * mib}, 1000, true},
		{"over", &registryserver.Usage{TotalBytes: 1200 * mib}, 1000, true},
	}
	for _, c := range cases {
		b, ok := registryQuotaBreach(4, c.usage, c.limitMB, 0.9)
		if ok != c.want {
			t.Errorf("%s: breach %v, want %v", c.name, ok, c.want)
			continue
		}
		if ok && (b.WorkspaceID != 4 || b.Unit != "MB" || b.Link != "/registry" || b.Limit != c.limitMB) {
			t.Errorf("%s: breach = %+v", c.name, b)
		}
	}
}
