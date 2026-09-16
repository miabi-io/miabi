// SPDX-FileCopyrightText: 2026 Jonas Kaninda
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
		// AdoptCentral marks the manager's gateway imported so Miabi never deploys a second one beside
		// it — but the stack installer hands that container the key. Reporting it would cry wolf on
		// every managed install, and a warning that always fires is one nobody reads.
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
