// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package edgegateway

import (
	"strings"

	"github.com/jkaninda/logger"
	"github.com/miabi-io/miabi/internal/models"
)

// MissingConfigKey names the nodes whose gateway will never receive GOMA_CONFIG_ENCRYPTION_KEY.
//
// Miabi injects the key into every gateway it deploys, but an imported one belongs to whoever
// installed it and is never redeployed — so once config encryption is on, Miabi encrypts middleware
// rules and TLS material those gateways cannot read, and their routes fail with no visible cause.
//
// The local node is excluded: AdoptCentral marks the manager's own gateway imported to stop Miabi
// deploying a second one beside it, but the stack installer gives that container the key.
func MissingConfigKey(servers []models.Server) []string {
	var out []string
	for _, s := range servers {
		if s.GatewayImported && !s.IsLocal {
			out = append(out, s.Name)
		}
	}
	return out
}

// WarnMissingConfigKey reports the problem loudly rather than quietly declining to encrypt. Silently
// downgrading would leave an operator believing the config is protected when it is not.
func WarnMissingConfigKey(key string, servers []models.Server) {
	if strings.TrimSpace(key) == "" {
		return
	}
	stranded := MissingConfigKey(servers)
	if len(stranded) == 0 {
		return
	}
	logger.Error("gateway config encryption is on, but these nodes run an imported gateway Miabi cannot "+
		"hand the key to — their routes will fail to load until GOMA_CONFIG_ENCRYPTION_KEY is set on those "+
		"gateways, or they are replaced by a Miabi-managed gateway",
		"nodes", strings.Join(stranded, ", "))
}
