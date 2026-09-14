// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package edgegateway

import (
	"testing"

	"github.com/miabi-io/miabi/internal/models"
)

// Regression: every agent reconnect recreated mb-node-gateway beside an imported gateway, which either
// failed on the ingress ports and left a stopped container, or started a second gateway.
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
