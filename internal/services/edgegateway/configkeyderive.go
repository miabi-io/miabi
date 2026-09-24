// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package edgegateway

import (
	"strconv"

	"github.com/miabi-io/miabi/internal/models"
	"github.com/miabi-io/miabi/internal/services/crypto"
)

// configKeyLabel prefixes the per-node derivation. Changing it re-keys every node, which takes
// effect only as each gateway is redeployed with its new env — so never casually.
const configKeyLabel = "gateway:config:"

// ConfigKey is the config-encryption passphrase for the gateway on srv.
//
// Per node, not per install. One shared key meant any gateway could decrypt any other's config, so
// a single compromised edge host read every tenant's middleware rules and TLS material — not only
// the ones it was sent. Derived per node, a stolen key is worth exactly the node it came from.
//
// Derived from the MASTER key rather than from the node's API token, deliberately: the token is
// rotatable, and rotating a credential must not silently re-key a config the gateway already holds
// and can no longer read. Keyed by node ID rather than name, so a rename does not re-key either.
//
// Two gateways keep the central passphrase instead:
//
//   - the manager's, which ships with the control plane and whose environment the operator owns;
//   - an imported one, which Miabi never redeploys and so can never hand a derived key.
//
// Both get `central` — whatever GOMA_CONFIG_ENCRYPTION_KEY is set to, which is also "" when config
// encryption is off entirely, the same answer as before this existed.
func ConfigKey(srv *models.Server, central string) string {
	if srv == nil || isManager(srv) || srv.GatewayImported {
		return central
	}
	// Encryption is a single install-wide switch: with no central key configured, nothing is
	// encrypted anywhere. Deriving here regardless would encrypt node configs an operator never
	// asked to encrypt, and the gateways would be handed a key for content that has none.
	if central == "" {
		return ""
	}
	return crypto.DeriveToken(configKeyLabel + strconv.FormatUint(uint64(srv.ID), 10))
}
