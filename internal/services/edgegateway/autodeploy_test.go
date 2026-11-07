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
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// Every agent reconnect used to recreate mb-node-gateway beside an imported gateway.
func TestAutoDeploySkipsImportedGateways(t *testing.T) {
	for name, tc := range map[string]struct {
		srv  *models.Server
		want bool
	}{
		"managed edge gateway":  {&models.Server{Connectivity: models.ConnectivityEdgeGateway}, true},
		"imported edge gateway": {&models.Server{Connectivity: models.ConnectivityEdgeGateway, GatewayImported: true, GatewayContainer: "goma"}, false},
		"cluster connectivity":  {&models.Server{Connectivity: models.ConnectivityCluster}, false},
		"no node":               {nil, false},
	} {
		if got := AutoDeploy(tc.srv); got != tc.want {
			t.Errorf("%s: AutoDeploy = %v, want %v", name, got, tc.want)
		}
	}
}
