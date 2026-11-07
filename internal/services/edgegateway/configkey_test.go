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

package edgegateway

import (
	"reflect"
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

func TestMissingConfigKey(t *testing.T) {
	cases := map[string]struct {
		servers []models.Server
		want    []string
	}{
		"an operator's own gateway on a node": {
			servers: []models.Server{{Name: "edge-1", GatewayImported: true}},
			want:    []string{"edge-1"},
		},
		// The stack installer gives the manager's adopted gateway the key, so reporting it would always fire.
		"the manager's own adopted gateway": {
			servers: []models.Server{{Name: "manager", GatewayImported: true, IsLocal: true}},
			want:    nil,
		},
		"a gateway Miabi deploys": {
			servers: []models.Server{{Name: "edge-2"}},
			want:    nil,
		},
		"a mixed fleet": {
			servers: []models.Server{
				{Name: "manager", GatewayImported: true, IsLocal: true},
				{Name: "edge-1", GatewayImported: true},
				{Name: "edge-2"},
				{Name: "edge-3", GatewayImported: true},
			},
			want: []string{"edge-1", "edge-3"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := MissingConfigKey(tc.servers); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("MissingConfigKey = %v, want %v", got, tc.want)
			}
		})
	}
}
